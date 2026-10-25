package server

import (
	"crs/config"
	"crs/database"
	"crs/ghauth"
	"crs/ghauth/ghauthtest"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecretKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// linking is a hosted server that links GitHub accounts through a fake
// GitHub: gh answers the device flow, the token endpoint, GET /user and
// revocation; api is the REST API the writes go to.
type linking struct {
	db   *database.DB
	gh   *ghauthtest.Server
	api  *fakeGitHub
	addr string
	key  ghauth.Key
}

// setupLinking configures linking — GitHubOAuthClientID in the config file,
// CRS_SECRET_KEY, and the client secret when clientSecret — and serves
// remote connections.
func setupLinking(t *testing.T, clientSecret bool) *linking {
	t.Helper()
	db := remoteSetup(t)
	useTempConfig(t, fmt.Sprintf("SleepDuration = 10\nGitHubOAuthClientID = %q\n", ghauthtest.ClientID))
	t.Setenv(secretKeyEnv, testSecretKey)
	secret := ""
	if clientSecret {
		secret = ghauthtest.ClientSecret
	}
	t.Setenv(oauthClientSecretEnv, secret)

	gh := ghauthtest.NewServer(t)
	origApp, origFlows := newLinkApp, githubLinks
	newLinkApp = func(clientID, clientSecret, scopes string) *ghauth.App {
		app := gh.App()
		app.ClientID, app.ClientSecret, app.Scopes = clientID, clientSecret, scopes
		return app
	}
	githubLinks = newLinkFlows(maxLinkFlows)
	t.Cleanup(func() {
		// No flow outlives the fake GitHub it polls.
		stopAllLinks(githubLinks)
		newLinkApp, githubLinks = origApp, origFlows
	})
	key, err := ghauth.ParseKey(testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	return &linking{db: db, gh: gh, api: useFakeGitHub(t), addr: startListener(t, defaultListenOptions), key: key}
}

func stopAllLinks(l *linkFlows) {
	l.mu.Lock()
	var logins []string
	for _, f := range l.flows {
		logins = append(logins, f.login)
	}
	l.mu.Unlock()
	for _, login := range logins {
		l.stop(login, true)
	}
}

// elsewhere is a workflow on another repository than the test PR's. A submit
// reprocesses the PR in the background for every workflow that lists its
// repo, and one still running when the test ends would outlive the fake
// GitHub.
func elsewhere() config.RawWorkflow {
	wf := reviewWorkflow("review")
	wf.Repos = []string{"acme/elsewhere"}
	return wf
}

// connect registers login over a new connection and returns it with the
// user's bearer token.
func (lt *linking) connect(t *testing.T, login string) (*remote, string) {
	t.Helper()
	r := dialRemote(t, lt.addr)
	return r, r.register(registration(login, "", elsewhere())).Token
}

// waitForLink polls GetGitHubLink until the link is no longer pending, and
// fails the test unless it ended as want.
func waitForLink(t *testing.T, r *remote, want string) *GetGitHubLinkReply {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		reply := &GetGitHubLinkReply{}
		r.must("GetGitHubLink", &GetGitHubLinkArgs{}, reply)
		if reply.Status != linkPending {
			if reply.Status != want {
				t.Fatalf("the link ended %s (%s), want %s", reply.Status, reply.Message, want)
			}
			return reply
		}
		if time.Now().After(deadline) {
			t.Fatalf("the link is still pending after 5s: %s", reply.Message)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// start calls StartGitHubLink and fails the test unless a flow is pending.
func startLink(t *testing.T, r *remote) *StartGitHubLinkReply {
	t.Helper()
	reply := &StartGitHubLinkReply{}
	r.must("StartGitHubLink", &StartGitHubLinkArgs{}, reply)
	if !reply.Okay || reply.Status != linkPending {
		t.Fatalf("StartGitHubLink = %+v", reply)
	}
	return reply
}

// link registers login and links their GitHub account.
func (lt *linking) link(t *testing.T, login string) (*remote, string) {
	t.Helper()
	r, token := lt.connect(t, login)
	lt.gh.GrantAs(login)
	lt.gh.Script("ok")
	startLink(t, r)
	waitForLink(t, r, linkLinked)
	return r, token
}

// stored opens login's stored credential; ok is false when there is none.
func (lt *linking) stored(t *testing.T, login string) (ghauth.Credential, bool) {
	t.Helper()
	sealed, err := lt.db.GetUserGitHubCredential(login)
	if err != nil {
		t.Fatalf("GetUserGitHubCredential(%s): %v", login, err)
	}
	if sealed == "" {
		return ghauth.Credential{}, false
	}
	cred, err := lt.key.Open(login, sealed)
	if err != nil {
		t.Fatalf("%s's stored credential does not open: %v", login, err)
	}
	return cred, true
}

func draftCount(t *testing.T, db *database.DB, login string) int {
	t.Helper()
	drafts, err := db.GetLocalCommentsForPR(login, aiOwner, aiRepo, aiNumber)
	if err != nil {
		t.Fatal(err)
	}
	return len(drafts)
}

// addDrafts adds a draft on a line, a reply to a GitHub comment, and an empty
// draft.
func (r *remote) addDrafts() {
	r.t.Helper()
	r.must("AddComment", &AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 2, Body: "a draft"}, &AddCommentReply{})
	r.must("AddComment", &AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 2, Body: "a reply", ReplyToID: ptr(6001)}, &AddCommentReply{})
	r.must("AddComment", &AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 3}, &AddCommentReply{})
}

// The device flow end to end: one flow per user however often it is
// started, a link stored only sealed, and the reply flags following it.
func TestLinkGitHubAccount(t *testing.T) {
	lt := setupLinking(t, true)
	r, token := lt.connect(t, "alice")

	auth := r.authenticate(token)
	if !auth.GitHubLinkAvailable || auth.GitHubLinked {
		t.Errorf("before linking: Authenticate flags = %+v", auth.githubLinkFields)
	}
	before := &GetGitHubLinkReply{}
	r.must("GetGitHubLink", &GetGitHubLinkArgs{}, before)
	if !before.Okay || before.Status != linkUnlinked || before.GitHubLogin != "" {
		t.Errorf("before linking: GetGitHubLink = %+v", before)
	}

	// Nothing scripted yet: the flow waits on the user.
	lt.gh.GrantAs("Alice")
	started := time.Now()
	first := startLink(t, r)
	expires, err := time.Parse(time.RFC3339, first.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q is not RFC 3339: %v", first.ExpiresAt, err)
	}
	if first.UserCode != ghauthtest.UserCode || first.VerificationURI != ghauthtest.VerificationURI || first.Interval != 1 ||
		expires.Before(started.Add(-time.Second)) || !strings.Contains(first.Message, ghauthtest.UserCode) {
		t.Errorf("StartGitHubLink = %+v", first)
	}
	// Starting again — from this connection or another of the user's —
	// returns the flow in progress rather than a second one.
	other := dialRemote(t, lt.addr)
	other.authenticate(token)
	var wg sync.WaitGroup
	for _, c := range []*remote{r, other, r, other} {
		wg.Add(1)
		go func(c *remote) {
			defer wg.Done()
			again := &StartGitHubLinkReply{}
			if err := c.call("StartGitHubLink", &StartGitHubLinkArgs{}, again); err != nil || again.UserCode != first.UserCode || again.ExpiresAt != first.ExpiresAt {
				t.Errorf("starting again: %+v, %v", again, err)
			}
		}(c)
	}
	wg.Wait()
	if scopes := lt.gh.Scopes(); len(scopes) != 1 || scopes[0] != "repo" {
		t.Errorf("GitHub was asked for %d flows with scopes %q, want one asking for repo", len(scopes), scopes)
	}
	pending := &GetGitHubLinkReply{}
	r.must("GetGitHubLink", &GetGitHubLinkArgs{}, pending)
	if pending.Status != linkPending || pending.UserCode != first.UserCode || pending.ExpiresAt != first.ExpiresAt {
		t.Errorf("while pending: GetGitHubLink = %+v", pending)
	}

	lt.gh.Script("slow_down", "ok")
	linked := waitForLink(t, r, linkLinked)
	// slow_down added five seconds to the interval of one.
	polls, answers := lt.gh.Polls(), lt.gh.Answers()
	if i := slices.Index(answers, "slow_down"); i < 0 || i+1 >= len(polls) {
		t.Errorf("polls answered %v", answers)
	} else if gap := polls[i+1].Sub(polls[i]); gap < 6*ghauthtest.Second-ghauthtest.Second/2 {
		t.Errorf("the poll after slow_down came %v after it, want at least %v", gap, 6*ghauthtest.Second)
	}
	if !linked.Okay || linked.GitHubLogin != "Alice" || linked.UserCode != "" {
		t.Errorf("once linked: GetGitHubLink = %+v", linked)
	}

	// Stored sealed: not the token, and opening only for alice's row with
	// this server's key.
	sealed, err := lt.db.GetUserGitHubCredential("alice")
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := lt.stored(t, "alice")
	if !ok || !strings.HasPrefix(cred.AccessToken, "gho_") || cred.GitHubLogin != "Alice" || cred.LinkedAt.IsZero() {
		t.Fatalf("stored credential: linked %v, login %q, linked at %v", ok, cred.GitHubLogin, cred.LinkedAt)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, cred.AccessToken) {
		t.Errorf("the stored column is %q", sealed)
	}
	if _, err := lt.key.Open("bob", sealed); !errors.Is(err, ghauth.ErrUnreadable) {
		t.Errorf("opening alice's credential as bob's: err = %v", err)
	}
	otherKey, _ := ghauth.ParseKey(strings.Repeat("ab", 32))
	if _, err := otherKey.Open("alice", sealed); !errors.Is(err, ghauth.ErrUnreadable) {
		t.Errorf("opening alice's credential with another key: err = %v", err)
	}

	if auth := r.authenticate(token); !auth.GitHubLinked || !auth.GitHubLinkAvailable {
		t.Errorf("after linking: Authenticate flags = %+v", auth.githubLinkFields)
	}
	mine := &GetMyWorkflowsReply{}
	r.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, mine)
	if !mine.GitHubLinked || !mine.GitHubLinkAvailable {
		t.Errorf("after linking: GetMyWorkflows flags = %+v", mine.githubLinkFields)
	}
	again := r.register(registration("alice", token, elsewhere()))
	if !again.GitHubLinked {
		t.Errorf("re-registering: RegisterUser flags = %+v; the link should survive it", again.githubLinkFields)
	}

	admin := dialRemote(t, lt.addr)
	admin.authenticate(testAdminToken)
	users := &ListUsersReply{}
	admin.must("ListUsers", &ListUsersArgs{}, users)
	if len(users.Users) != 1 || !users.Users[0].GitHubLinked || users.Users[0].GitHubLogin != "Alice" {
		t.Errorf("ListUsers = %+v", users.Users)
	}
	raw, _ := json.Marshal(users)
	if strings.Contains(string(raw), cred.AccessToken) || strings.Contains(string(raw), "v1:") {
		t.Errorf("ListUsers shows the credential: %s", raw)
	}
}

// A flow that ends without a link says how, stores nothing, and can be
// started again.
func TestLinkGitHubOutcomes(t *testing.T) {
	lt := setupLinking(t, true)
	r, _ := lt.connect(t, "alice")

	for _, tc := range []struct {
		name   string
		script []string
		grant  string
		timing [2]int
		want   string
	}{
		{name: "declined", script: []string{"authorization_pending", "access_denied"}, want: linkDenied},
		{name: "expired by GitHub", script: []string{"expired_token"}, want: linkExpired},
		{name: "expired by the clock", timing: [2]int{1, 3}, want: linkExpired},
		{name: "another account", script: []string{"ok"}, grant: "mallory", want: linkMismatch},
		{name: "device flow disabled", script: []string{"device_flow_disabled"}, want: linkError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := tc.grant
			if grant == "" {
				grant = "alice"
			}
			lt.gh.GrantAs(grant)
			if tc.timing != [2]int{} {
				lt.gh.Timing(tc.timing[0], tc.timing[1])
				defer lt.gh.Timing(1, 900)
			}
			lt.gh.Script(tc.script...)
			startLink(t, r)
			ended := waitForLink(t, r, tc.want)
			if !ended.Okay || ended.Message == "" || ended.GitHubLogin != "" {
				t.Errorf("GetGitHubLink = %+v", ended)
			}
			if _, linked := lt.stored(t, "alice"); linked {
				t.Error("a link was stored")
			}
		})
	}

	// The token granted for another account was revoked, not left behind.
	if revoked := lt.gh.Revoked(); len(revoked) != 1 || lt.gh.Valid(revoked[0]) {
		t.Errorf("revoked %v, want the mismatched token", revoked)
	}
	// And a failed flow does not stand in the way of the next.
	lt.gh.GrantAs("alice")
	lt.gh.Script("ok")
	startLink(t, r)
	waitForLink(t, r, linkLinked)
}

// GitHub accounts are linked by registered users on a server set up for it.
func TestLinkGitHubUnavailable(t *testing.T) {
	lt := setupLinking(t, false)
	r, token := lt.connect(t, "alice")

	// The administrator token's session and the local connection are the
	// server's own identity.
	admin := dialRemote(t, lt.addr)
	if auth := admin.authenticate(testAdminToken); auth.GitHubLinkAvailable || auth.GitHubLinked {
		t.Errorf("the administrator token's Authenticate flags = %+v", auth.githubLinkFields)
	}
	for name, h := range map[string]func(string, any, any) error{
		"administrator": admin.call,
		"local": func(method string, args, reply any) error {
			local := &RPCHandler{}
			switch method {
			case "StartGitHubLink":
				return local.StartGitHubLink(args.(*StartGitHubLinkArgs), reply.(*StartGitHubLinkReply))
			case "GetGitHubLink":
				return local.GetGitHubLink(args.(*GetGitHubLinkArgs), reply.(*GetGitHubLinkReply))
			}
			return local.UnlinkGitHub(args.(*UnlinkGitHubArgs), reply.(*UnlinkGitHubReply))
		},
	} {
		start, get, unlink := &StartGitHubLinkReply{}, &GetGitHubLinkReply{}, &UnlinkGitHubReply{}
		if err := h("StartGitHubLink", &StartGitHubLinkArgs{}, start); err != nil || start.Okay || start.Status != linkUnavailable || start.Message == "" {
			t.Errorf("%s StartGitHubLink = %+v, %v", name, start, err)
		}
		if err := h("GetGitHubLink", &GetGitHubLinkArgs{}, get); err != nil || get.Okay || get.Status != linkUnavailable || get.Message == "" {
			t.Errorf("%s GetGitHubLink = %+v, %v", name, get, err)
		}
		if err := h("UnlinkGitHub", &UnlinkGitHubArgs{}, unlink); err != nil || unlink.Okay || unlink.Message == "" {
			t.Errorf("%s UnlinkGitHub = %+v, %v", name, unlink, err)
		}
	}

	// A server missing the client ID or a usable key cannot link.
	for name, configure := range map[string]func(t *testing.T){
		"no client ID": func(t *testing.T) {
			cfg := config.C()
			t.Cleanup(func() { config.SetC(cfg) })
			cfg.GitHubOAuthClientID = ""
			config.SetC(cfg)
		},
		"no secret key":      func(t *testing.T) { t.Setenv(secretKeyEnv, "") },
		"invalid secret key": func(t *testing.T) { t.Setenv(secretKeyEnv, "not-a-key") },
	} {
		t.Run(name, func(t *testing.T) {
			configure(t)
			start := &StartGitHubLinkReply{}
			r.must("StartGitHubLink", &StartGitHubLinkArgs{}, start)
			if start.Okay || start.Status != linkUnavailable || start.Message == "" {
				t.Errorf("StartGitHubLink = %+v", start)
			}
			get := &GetGitHubLinkReply{}
			r.must("GetGitHubLink", &GetGitHubLinkArgs{}, get)
			if get.Okay || get.Status != linkUnavailable {
				t.Errorf("GetGitHubLink = %+v", get)
			}
			if auth := r.authenticate(token); auth.GitHubLinkAvailable {
				t.Errorf("Authenticate says linking is available")
			}
			// Without linking, a remote write is the plain refusal.
			wantError(t, "SubmitReview", r.call("SubmitReview", &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT"}, &SubmitReviewReply{}),
				errServerIdentityRefused.Error())
		})
	}
	if scopes := lt.gh.Scopes(); len(scopes) != 0 {
		t.Errorf("GitHub was asked for %d device flows by sessions that cannot link", len(scopes))
	}
}

// A linked user's review, its replies and their merge go to GitHub under the
// user's own token; everything the server reads around them stays on its
// own.
func TestLinkedUserWritesAsThemselves(t *testing.T) {
	lt := setupLinking(t, true)
	alice, _ := lt.link(t, "alice")
	cred, _ := lt.stored(t, "alice")
	alice.addDrafts()

	alice.must("SubmitReview", &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "APPROVE"}, &SubmitReviewReply{})
	merged := &MergePRReply{}
	alice.must("MergePR", &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, merged)
	if !merged.Merged {
		t.Errorf("MergePR = %+v", merged)
	}

	writes, reads := lt.api.auths()
	userAuth := "Bearer " + cred.AccessToken
	if len(writes) != 3 {
		t.Fatalf("GitHub received %d writes, want the reply, the review and the merge", len(writes))
	}
	for i, auth := range writes {
		if auth != userAuth {
			t.Errorf("write %d went out as %q, want alice's token", i, auth)
		}
	}
	if len(reads) == 0 {
		t.Error("the submit refetched nothing")
	}
	for i, auth := range reads {
		if auth != "Bearer test-token" {
			t.Errorf("read %d went out as %q, want the server's token", i, auth)
		}
	}
	lt.api.mu.Lock()
	reviews, replies := len(lt.api.reviews), len(lt.api.replies)
	lt.api.mu.Unlock()
	if reviews != 1 || replies != 1 {
		t.Errorf("GitHub received %d reviews and %d replies, want 1 and 1", reviews, replies)
	}
	if n := draftCount(t, lt.db, "alice"); n != 0 {
		t.Errorf("alice has %d drafts after submitting", n)
	}
}

// A linked token GitHub no longer accepts is cleared at the first write that
// finds out, and the user is asked to link again; their drafts are kept.
func TestRevokedLinkIsCleared(t *testing.T) {
	lt := setupLinking(t, true)
	alice, token := lt.link(t, "alice")
	cred, _ := lt.stored(t, "alice")
	alice.addDrafts()
	lt.api.reject(cred.AccessToken)

	submit := &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT", Body: "LGTM"}
	wantError(t, "SubmitReview", alice.call("SubmitReview", submit, &SubmitReviewReply{}), errLinkExpired.Error())
	if n := draftCount(t, lt.db, "alice"); n != 3 {
		t.Errorf("a refused submit left %d drafts, want all 3, the empty one too", n)
	}
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("the refused credential is still stored")
	}
	if auth := alice.authenticate(token); auth.GitHubLinked {
		t.Error("Authenticate still says linked")
	}
	get := &GetGitHubLinkReply{}
	alice.must("GetGitHubLink", &GetGitHubLinkArgs{}, get)
	if get.Status != linkUnlinked {
		t.Errorf("GetGitHubLink = %+v", get)
	}
	// From now on the user has nothing linked.
	wantError(t, "SubmitReview again", alice.call("SubmitReview", submit, &SubmitReviewReply{}), errLinkRequired.Error())

	// A merge finds out the same way.
	lt.gh.Script("ok")
	startLink(t, alice)
	waitForLink(t, alice, linkLinked)
	cred, _ = lt.stored(t, "alice")
	lt.api.reject(cred.AccessToken)
	wantError(t, "MergePR", alice.call("MergePR", &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &MergePRReply{}), errLinkExpired.Error())
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("the credential a merge found refused is still stored")
	}

	lt.api.mu.Lock()
	defer lt.api.mu.Unlock()
	if len(lt.api.reviews) != 0 || lt.api.merges != 0 {
		t.Errorf("GitHub recorded %d reviews and %d merges from refused tokens", len(lt.api.reviews), lt.api.merges)
	}
}

// A user without a link is asked to link one, unless the server lends its
// own account — and then only an administrator merges with it.
func TestUnlinkedUserWrites(t *testing.T) {
	lt := setupLinking(t, true)
	alice, _ := lt.connect(t, "alice")
	alice.addDrafts()
	submit := &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT"}
	merge := &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}

	wantError(t, "SubmitReview", alice.call("SubmitReview", submit, &SubmitReviewReply{}), errLinkRequired.Error())
	wantError(t, "MergePR", alice.call("MergePR", merge, &MergePRReply{}), errLinkRequired.Error())
	if n := draftCount(t, lt.db, "alice"); n != 3 {
		t.Errorf("a refused submit left %d drafts, want 3", n)
	}
	// The administrator token's session has no account to link: the plain
	// refusal.
	admin := dialRemote(t, lt.addr)
	admin.authenticate(testAdminToken)
	wantError(t, "an administrator's MergePR", admin.call("MergePR", merge, &MergePRReply{}), errServerIdentityRefused.Error())

	allowServerIdentityActions(t)
	alice.must("SubmitReview", submit, &SubmitReviewReply{})
	wantError(t, "a user's MergePR as the server", alice.call("MergePR", merge, &MergePRReply{}), errMergeAsServerNeedsAdmin.Error())
	admin.must("MergePR", merge, &MergePRReply{})
	writes, _ := lt.api.auths()
	if len(writes) != 3 {
		t.Fatalf("GitHub received %d writes, want the reply, the review and the administrator's merge", len(writes))
	}
	for i, auth := range writes {
		if auth != "Bearer test-token" {
			t.Errorf("write %d went out as %q, want the server's token", i, auth)
		}
	}

	// A user who links writes as themselves even where the server would lend
	// its account.
	bob, _ := lt.link(t, "bob")
	bob.must("MergePR", merge, &MergePRReply{})
	cred, _ := lt.stored(t, "bob")
	if writes, _ := lt.api.auths(); writes[len(writes)-1] != "Bearer "+cred.AccessToken {
		t.Errorf("bob's merge went out as %q", writes[len(writes)-1])
	}
}

// storeCredential replaces login's stored credential with cred, as if it
// had been linked that way.
func (lt *linking) storeCredential(t *testing.T, login, token string, cred ghauth.Credential) {
	t.Helper()
	sealed, err := lt.key.Seal(login, cred)
	if err != nil {
		t.Fatal(err)
	}
	if err := lt.db.SetUserGitHubCredential(login, hashToken(token), sealed); err != nil {
		t.Fatal(err)
	}
}

// A GitHub App's token is refreshed before a write when it is about to
// expire — once, however many writes ask at the same time.
func TestExpiringLinkIsRefreshed(t *testing.T) {
	lt := setupLinking(t, true)
	alice, token := lt.connect(t, "alice")
	now := time.Now()
	linkedAt := now.Add(-8 * time.Hour).UTC()
	lt.gh.AddToken("gho_old", "ghr_old", "alice")
	lt.storeCredential(t, "alice", token, ghauth.Credential{
		Token: ghauth.Token{AccessToken: "gho_old", TokenType: "bearer", RefreshToken: "ghr_old",
			ExpiresAt: now.Add(time.Minute), RefreshTokenExpiresAt: now.Add(1000 * time.Hour)},
		GitHubLogin: "alice", LinkedAt: linkedAt,
	})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := alice.call("MergePR", &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &MergePRReply{}); err != nil {
				t.Errorf("MergePR: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := lt.gh.Refreshes(); n != 1 {
		t.Errorf("refreshed %d times, want once", n)
	}
	cred, _ := lt.stored(t, "alice")
	if cred.AccessToken == "gho_old" || cred.RefreshToken == "ghr_old" || !cred.ExpiresAt.After(now.Add(time.Hour)) ||
		cred.GitHubLogin != "alice" || !cred.LinkedAt.Equal(linkedAt) {
		t.Errorf("after refreshing: token %q, refresh %q, expires %v, linked at %v", cred.AccessToken, cred.RefreshToken, cred.ExpiresAt, cred.LinkedAt)
	}
	writes, _ := lt.api.auths()
	for i, auth := range writes {
		if auth != "Bearer "+cred.AccessToken {
			t.Errorf("merge %d went out as %q, want the refreshed token", i, auth)
		}
	}

	// A refresh token GitHub refuses ends the link.
	lt.storeCredential(t, "alice", token, ghauth.Credential{
		Token:       ghauth.Token{AccessToken: "gho_dead", RefreshToken: "ghr_spent", ExpiresAt: now.Add(-time.Minute)},
		GitHubLogin: "alice",
	})
	wantError(t, "MergePR with a spent refresh token", alice.call("MergePR", &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &MergePRReply{}),
		errLinkExpired.Error())
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("a link GitHub would not refresh is still stored")
	}
}

// Without the client secret an expired token cannot be refreshed: the link
// ends. One not yet expired is used as it is.
func TestExpiringLinkWithoutClientSecret(t *testing.T) {
	lt := setupLinking(t, false)
	alice, token := lt.connect(t, "alice")
	now := time.Now()
	merge := &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}

	lt.storeCredential(t, "alice", token, ghauth.Credential{
		Token:       ghauth.Token{AccessToken: "gho_soon", RefreshToken: "ghr_soon", ExpiresAt: now.Add(time.Minute)},
		GitHubLogin: "alice",
	})
	alice.must("MergePR", merge, &MergePRReply{})
	if writes, _ := lt.api.auths(); len(writes) != 1 || writes[0] != "Bearer gho_soon" {
		t.Errorf("writes = %v, want one with the token about to expire", writes)
	}

	lt.storeCredential(t, "alice", token, ghauth.Credential{
		Token:       ghauth.Token{AccessToken: "gho_gone", RefreshToken: "ghr_gone", ExpiresAt: now.Add(-time.Minute)},
		GitHubLogin: "alice",
	})
	wantError(t, "MergePR with an expired token", alice.call("MergePR", merge, &MergePRReply{}), errLinkExpired.Error())
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("an expired link is still stored")
	}
	if lt.gh.Refreshes() != 0 {
		t.Error("refreshed without the client secret")
	}
}

// Without GitHubOAuthClientID nothing can refresh a GitHub App's token, so
// the link lapses once it expires. The server cannot link an account either
// then, so the refusal does not send the user to a link dialog.
func TestExpiredLinkWithoutClientID(t *testing.T) {
	lt := setupLinking(t, true)
	alice, token := lt.connect(t, "alice")
	lt.storeCredential(t, "alice", token, ghauth.Credential{
		Token:       ghauth.Token{AccessToken: "gho_gone", RefreshToken: "ghr_gone", ExpiresAt: time.Now().Add(-time.Minute)},
		GitHubLogin: "alice",
	})
	useTempConfig(t, "SleepDuration = 10\n")

	wantError(t, "MergePR with an expired token and no client ID", alice.call("MergePR", &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &MergePRReply{}),
		errLinkUnusable.Error())
	if lt.gh.Refreshes() != 0 {
		t.Error("refreshed without a client ID")
	}
	if writes, _ := lt.api.auths(); len(writes) != 0 {
		t.Errorf("writes = %v, want none", writes)
	}
	if !strings.HasPrefix(errLinkUnusable.Error(), "refused: ") || strings.HasPrefix(errLinkUnusable.Error(), "refused: link-github:") {
		t.Errorf("%q should be final: refused, not link-github", errLinkUnusable)
	}
}

// A link the server cannot open because CRS_SECRET_KEY is missing — a
// restart that lost it — is refused like one sealed under another key, never
// quietly replaced by the server's account.
func TestLinkWithoutTheSecretKey(t *testing.T) {
	lt := setupLinking(t, true)
	alice, _ := lt.link(t, "alice")
	allowServerIdentityActions(t)
	t.Setenv(secretKeyEnv, "")

	wantError(t, "SubmitReview without the key", alice.call("SubmitReview", &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT", Body: "LGTM"}, &SubmitReviewReply{}),
		errLinkUnusable.Error())
	if writes, _ := lt.api.auths(); len(writes) != 0 {
		t.Errorf("writes = %v, want none: the review went out as the server's account", writes)
	}
	if _, linked := lt.stored(t, "alice"); !linked {
		t.Error("the link was cleared; it works again once the key is back")
	}

	// Once the key is back, so is the link.
	t.Setenv(secretKeyEnv, testSecretKey)
	alice.must("SubmitReview", &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT", Body: "LGTM"}, &SubmitReviewReply{})
	if writes, _ := lt.api.auths(); len(writes) == 0 || writes[len(writes)-1] == "Bearer test-token" {
		t.Errorf("writes = %v, want alice's own token", writes)
	}
}

// A stored credential this server's key cannot open — the key changed — is
// reported, and refused for writes rather than replaced by the server's
// account.
func TestUnreadableLink(t *testing.T) {
	lt := setupLinking(t, true)
	alice, token := lt.connect(t, "alice")
	otherKey, _ := ghauth.ParseKey(strings.Repeat("ab", 32))
	sealed, _ := otherKey.Seal("alice", ghauth.Credential{Token: ghauth.Token{AccessToken: "gho_x"}, GitHubLogin: "alice"})
	if err := lt.db.SetUserGitHubCredential("alice", hashToken(token), sealed); err != nil {
		t.Fatal(err)
	}
	allowServerIdentityActions(t)

	get := &GetGitHubLinkReply{}
	alice.must("GetGitHubLink", &GetGitHubLinkArgs{}, get)
	if get.Status != linkError || !strings.Contains(get.Message, secretKeyEnv) {
		t.Errorf("GetGitHubLink = %+v", get)
	}
	if auth := alice.authenticate(token); auth.GitHubLinked {
		t.Error("Authenticate says linked")
	}
	wantError(t, "SubmitReview", alice.call("SubmitReview", &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT"}, &SubmitReviewReply{}),
		errLinkUnreadable.Error())
	if !strings.HasPrefix(errLinkUnreadable.Error(), "refused: link-github: ") {
		t.Errorf("%q lacks the prefix clients offer linking on", errLinkUnreadable)
	}

	// Linking again replaces it.
	lt.gh.GrantAs("alice")
	lt.gh.Script("ok")
	startLink(t, alice)
	waitForLink(t, alice, linkLinked)
}

// Unlinking deletes the stored credential, revoking it on GitHub when the
// server has the client secret, and cancels a link in progress.
func TestUnlinkGitHub(t *testing.T) {
	lt := setupLinking(t, true)
	alice, token := lt.link(t, "alice")
	cred, _ := lt.stored(t, "alice")

	unlink := &UnlinkGitHubReply{}
	alice.must("UnlinkGitHub", &UnlinkGitHubArgs{}, unlink)
	if !unlink.Okay || !strings.Contains(unlink.Message, "revoked on GitHub") {
		t.Errorf("UnlinkGitHub = %+v", unlink)
	}
	if revoked := lt.gh.Revoked(); len(revoked) != 1 || revoked[0] != cred.AccessToken {
		t.Errorf("revoked %v, want alice's token", revoked)
	}
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("still linked after unlinking")
	}
	if auth := alice.authenticate(token); auth.GitHubLinked {
		t.Error("Authenticate still says linked")
	}
	alice.must("UnlinkGitHub", &UnlinkGitHubArgs{}, unlink)
	if !unlink.Okay || unlink.Message != "No GitHub account was linked" {
		t.Errorf("unlinking twice = %+v", unlink)
	}

	// A link in progress is cancelled, and nothing it was granted is stored.
	startLink(t, alice)
	alice.must("UnlinkGitHub", &UnlinkGitHubArgs{}, unlink)
	if !unlink.Okay || !strings.Contains(unlink.Message, "Cancelled") {
		t.Errorf("unlinking a link in progress = %+v", unlink)
	}
	polls := len(lt.gh.Polls())
	lt.gh.Script("ok")
	time.Sleep(5 * ghauthtest.Second)
	if len(lt.gh.Polls()) != polls {
		t.Error("the cancelled flow is still polling GitHub")
	}
	get := &GetGitHubLinkReply{}
	alice.must("GetGitHubLink", &GetGitHubLinkArgs{}, get)
	if get.Status != linkUnlinked {
		t.Errorf("after cancelling: GetGitHubLink = %+v", get)
	}
}

// Without the client secret, unlinking still forgets the token, and says it
// could not revoke it.
func TestUnlinkGitHubWithoutClientSecret(t *testing.T) {
	lt := setupLinking(t, false)
	alice, _ := lt.link(t, "alice")
	cred, _ := lt.stored(t, "alice")

	unlink := &UnlinkGitHubReply{}
	alice.must("UnlinkGitHub", &UnlinkGitHubArgs{}, unlink)
	if !unlink.Okay || !strings.Contains(unlink.Message, "could not be revoked") {
		t.Errorf("UnlinkGitHub = %+v", unlink)
	}
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("still linked after unlinking")
	}
	if len(lt.gh.Revoked()) != 0 || !lt.gh.Valid(cred.AccessToken) {
		t.Error("a token was revoked without the client secret")
	}
}

// Deleting a user revokes their linked token and cancels a link in progress;
// so does retention, through ForgetDeletedUsers.
func TestDeletingAUserEndsTheirLink(t *testing.T) {
	lt := setupLinking(t, true)
	_, _ = lt.link(t, "alice")
	aliceCred, _ := lt.stored(t, "alice")
	bob, _ := lt.connect(t, "bob")
	lt.gh.GrantAs("bob")
	startLink(t, bob)

	admin := dialRemote(t, lt.addr)
	admin.authenticate(testAdminToken)
	for _, login := range []string{"alice", "bob"} {
		deleted := &DeleteUserReply{}
		admin.must("DeleteUser", &DeleteUserArgs{Login: login}, deleted)
		if !deleted.Okay || (login == "alice") != strings.Contains(deleted.Message, "revoked on GitHub") {
			t.Errorf("DeleteUser(%s) = %+v", login, deleted)
		}
	}
	if revoked := lt.gh.Revoked(); len(revoked) != 1 || revoked[0] != aliceCred.AccessToken {
		t.Errorf("revoked %v, want alice's token", revoked)
	}
	// bob's flow is gone: GitHub granting it now stores nothing, even once he
	// is registered again (under a new token: DeleteUser revoked his).
	polls := len(lt.gh.Polls())
	lt.gh.Script("ok")
	bob.register(registration("bob", "", elsewhere()))
	time.Sleep(5 * ghauthtest.Second)
	if len(lt.gh.Polls()) != polls {
		t.Error("a deleted user's flow is still polling GitHub")
	}
	if _, linked := lt.stored(t, "bob"); linked {
		t.Error("a deleted user's flow stored a link")
	}
	get := &GetGitHubLinkReply{}
	bob.must("GetGitHubLink", &GetGitHubLinkArgs{}, get)
	if get.Status != linkUnlinked {
		t.Errorf("bob's link after deletion = %+v", get)
	}

	// Retention deletes in the workflow cycle, which hands the deleted users
	// to ForgetDeletedUsers.
	_, _ = lt.link(t, "carol")
	carolCred, _ := lt.stored(t, "carol")
	swept, err := lt.db.DeleteUsersIdleSinceWithCredentials(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ForgetDeletedUsers(swept)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(lt.gh.Revoked(), carolCred.AccessToken) {
		if time.Now().After(deadline) {
			t.Fatalf("carol's token was not revoked after retention deleted her; revoked %v", lt.gh.Revoked())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A link is stored only for the bearer token that started it: not for
// whoever registers the login after its user was deleted.
func TestLinkStoredOnlyForTheTokenThatStartedIt(t *testing.T) {
	lt := setupLinking(t, true)
	alice, token := lt.connect(t, "alice")
	lt.gh.GrantAs("alice")
	startLink(t, alice)

	// alice is deleted behind the RPC's back — so her flow is not cancelled —
	// and mallory registers the login before GitHub grants the flow.
	if err := lt.db.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	mallory := dialRemote(t, lt.addr)
	mallory.register(registration("alice", "", elsewhere()))
	// The flow in progress is alice's, not mallory's to see.
	get := &GetGitHubLinkReply{}
	mallory.must("GetGitHubLink", &GetGitHubLinkArgs{}, get)
	if get.Status != linkUnlinked || get.UserCode != "" {
		t.Errorf("mallory sees %+v", get)
	}

	lt.gh.Script("ok")
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, ok := githubLinks.lookup("alice", hashToken(token))
		if ok && v.status != linkPending {
			if v.status != linkError || !strings.Contains(v.message, "no longer registered") {
				t.Errorf("alice's flow ended %s: %s", v.status, v.message)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("alice's flow did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, linked := lt.stored(t, "alice"); linked {
		t.Error("a link alice started was stored on mallory's registration")
	}
	if revoked := lt.gh.Revoked(); len(revoked) != 1 {
		t.Errorf("revoked %v, want the unstored token", revoked)
	}
}

// The replies' keys are the contract clients are built against.
func TestGitHubLinkWireShapes(t *testing.T) {
	keys := func(v any) string {
		raw, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	for name, tc := range map[string]struct {
		reply any
		want  string
	}{
		"StartGitHubLink": {StartGitHubLinkReply{}, "expires_at,interval,message,okay,status,user_code,verification_uri"},
		"GetGitHubLink":   {GetGitHubLinkReply{}, "expires_at,github_login,message,okay,status,user_code,verification_uri"},
		"UnlinkGitHub":    {UnlinkGitHubReply{}, "message,okay"},
		"UserInfo":        {UserInfo{}, "created_at,github_linked,github_login,is_admin,last_seen,login,workflow_count"},
	} {
		if got := keys(tc.reply); got != tc.want {
			t.Errorf("%s keys = %s, want %s", name, got, tc.want)
		}
	}
	for _, err := range []error{errLinkRequired, errLinkExpired, errLinkUnreadable} {
		if !strings.HasPrefix(err.Error(), "refused: link-github: ") {
			t.Errorf("%q lacks the prefix clients offer linking on", err)
		}
	}
	if errLinkRequired.Error() != "refused: link-github: link your GitHub account to submit reviews as yourself" ||
		errLinkExpired.Error() != "refused: link-github: your GitHub authorization has expired or was revoked; link your account again" {
		t.Error("the link-github refusals changed")
	}
}
