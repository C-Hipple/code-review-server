package server

import (
	"bufio"
	"crs/config"
	"crs/database"
	"encoding/json"
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testRegistrationKey = "registration-key"
	testAdminToken      = "admin-token"
)

// remoteSetup prepares the server for remote connections: a fresh DB and
// config file (GetAllReviews reloads it), the two secrets, and a seeded PR.
func remoteSetup(t *testing.T) *database.DB {
	t.Helper()
	db := aiTestSetup(t, nil)
	useTempConfig(t, "SleepDuration = 10\n")
	t.Setenv(registrationKeyEnv, testRegistrationKey)
	t.Setenv(adminTokenEnv, testAdminToken)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	return db
}

// startListener serves remote connections on a loopback port, as crs --listen
// does, until the test ends.
func startListener(t *testing.T, opts listenOptions) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- serve(ln, opts) }()
	t.Cleanup(func() {
		ln.Close()
		if err := <-done; err != nil {
			t.Errorf("serve returned %v after the listener closed", err)
		}
	})
	return ln.Addr().String()
}

// remote is one client connection.
type remote struct {
	t      *testing.T
	client *rpc.Client
}

func dialRemote(t *testing.T, addr string) *remote {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client := jsonrpc.NewClient(conn)
	t.Cleanup(func() { client.Close() })
	return &remote{t: t, client: client}
}

// call calls an RPCHandler method and returns its error.
func (r *remote) call(method string, args, reply any) error {
	return r.client.Call(handlerServicePrefix+method, args, reply)
}

// must calls an RPCHandler method that has to succeed.
func (r *remote) must(method string, args, reply any) {
	r.t.Helper()
	if err := r.call(method, args, reply); err != nil {
		r.t.Fatalf("%s: %v", method, err)
	}
}

// register registers login over r and fails the test unless it succeeds.
func (r *remote) register(args *RegisterUserArgs) *RegisterUserReply {
	r.t.Helper()
	reply := &RegisterUserReply{}
	r.must("RegisterUser", args, reply)
	if !reply.Okay {
		r.t.Fatalf("RegisterUser(%s) refused: %s %v", args.Login, reply.Message, reply.Errors)
	}
	return reply
}

func (r *remote) authenticate(token string) *AuthenticateReply {
	r.t.Helper()
	reply := &AuthenticateReply{}
	r.must("Authenticate", &AuthenticateArgs{Token: token}, reply)
	return reply
}

func reviewWorkflow(name string) config.RawWorkflow {
	return config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: name, SectionTitle: "Needs Review",
		Repos: []string{aiOwner + "/" + aiRepo}}
}

func registration(login, token string, wfs ...config.RawWorkflow) *RegisterUserArgs {
	if wfs == nil {
		wfs = []config.RawWorkflow{reviewWorkflow("review")}
	}
	return &RegisterUserArgs{RegistrationKey: testRegistrationKey, Login: login, Token: token, Workflows: wfs}
}

func wantError(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("%s: err = %v, want %q", what, err, want)
	}
}

// Before authenticating, a connection may say hello and authenticate, and
// nothing else — whatever it asks for, administrator's methods and methods
// that do not exist included.
func TestRemoteCallsNeedAuthentication(t *testing.T) {
	db := remoteSetup(t)
	seedSectionItem(t, db, "", "Needs Review", 1, time.Now())
	r := dialRemote(t, startListener(t, defaultListenOptions))

	hello := &HelloReply{}
	r.must("Hello", &HelloArgs{}, hello)
	if hello.Count != 0 {
		t.Errorf("Hello counted %d sections before authenticating; the server's dashboard is not this connection's", hello.Count)
	}

	for _, method := range []string{"GetAllReviews", "GetPR", "GetMyWorkflows", "SubmitReview", "GetConfig", "ListUsers", "NoSuchMethod"} {
		err := r.call(method, &struct{}{}, &struct{}{})
		wantError(t, method, err, "not authenticated: RPCHandler."+method+
			" needs an authenticated connection; call RPCHandler.Authenticate or RPCHandler.RegisterUser first")
	}
	err := r.client.Call(refusalMethod, &struct{}{}, &struct{}{})
	if err == nil || !strings.HasPrefix(err.Error(), "not authenticated: ") {
		t.Errorf("calling the refusal service directly: err = %v", err)
	}

	for _, token := range []string{"", "crs_" + strings.Repeat("0", 64), "not-a-token"} {
		reply := r.authenticate(token)
		if reply.Okay || reply.Known {
			t.Errorf("Authenticate(%q) = %+v, want okay=false known=false", token, reply)
		}
	}
	err = r.call("GetAllReviews", &GetReviewsArgs{}, &GetReviewsReply{})
	if err == nil || !strings.HasPrefix(err.Error(), "not authenticated: ") {
		t.Errorf("after failed authentications: err = %v", err)
	}

	// The connection survived every refusal.
	r.must("Hello", &HelloArgs{}, &HelloReply{})
}

// The wire is stdio's: newline-delimited JSON-RPC 1.0, args under their
// PascalCase keys, replies under lowercase ones.
func TestRemoteWireShapes(t *testing.T) {
	remoteSetup(t)
	conn, err := net.Dial("tcp", startListener(t, defaultListenOptions))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	roundTrip := func(id int, method string, params string) map[string]any {
		t.Helper()
		fmt.Fprintf(conn, `{"id": %d, "method": %q, "params": [%s]}`+"\n", id, method, params)
		if !lines.Scan() {
			t.Fatalf("%s: no reply: %v", method, lines.Err())
		}
		var resp struct {
			ID     int             `json:"id"`
			Result map[string]any  `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(lines.Bytes(), &resp); err != nil {
			t.Fatalf("%s: reply is not one JSON object per line: %v\n%s", method, err, lines.Bytes())
		}
		if resp.ID != id || string(resp.Error) != "null" {
			t.Fatalf("%s: id %d, error %s", method, resp.ID, resp.Error)
		}
		return resp.Result
	}
	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}

	registered := roundTrip(1, "RPCHandler.RegisterUser", fmt.Sprintf(`{"RegistrationKey": %q, "Login": "octocat",
		"Workflows": [{"WorkflowType": "SyncReviewRequestsWorkflow", "Name": "review", "SectionTitle": "Needs Review", "Repos": ["acme/widgets"]}],
		"Repos": [], "SectionPriority": {"Needs Review": 1}, "SectionSorting": {}}`, testRegistrationKey))
	if got, want := keys(registered), []string{"created", "errors", "github_link_available", "github_linked", "is_admin", "login", "message", "okay", "token"}; !slices.Equal(got, want) {
		t.Errorf("RegisterUser reply keys = %v, want %v", got, want)
	}
	token, _ := registered["token"].(string)
	if !tokenPattern.MatchString(token) || registered["okay"] != true || registered["created"] != true {
		t.Fatalf("RegisterUser reply = %v", registered)
	}

	authenticated := roundTrip(2, "RPCHandler.Authenticate", fmt.Sprintf(`{"Token": %q}`, token))
	if got, want := keys(authenticated), []string{"github_link_available", "github_linked", "is_admin", "known", "login", "message", "okay"}; !slices.Equal(got, want) {
		t.Errorf("Authenticate reply keys = %v, want %v", got, want)
	}
	if authenticated["okay"] != true || authenticated["login"] != "octocat" {
		t.Errorf("Authenticate reply = %v", authenticated)
	}

	mine := roundTrip(3, "RPCHandler.GetMyWorkflows", `{}`)
	if got, want := keys(mine), []string{"filters", "github_link_available", "github_linked", "known", "login", "okay", "repos", "section_priority", "section_sorting", "workflow_types", "workflows"}; !slices.Equal(got, want) {
		t.Errorf("GetMyWorkflows reply keys = %v, want %v", got, want)
	}
	workflows, _ := mine["workflows"].([]any)
	if len(workflows) != 1 || workflows[0].(map[string]any)["Name"] != "review" {
		t.Errorf("GetMyWorkflows workflows = %v, want the one sent, under its Go field names", mine["workflows"])
	}
}

// Users who name their workflows alike still each get their own: their own
// stored list, dashboard and drafts.
func TestRemoteUsersSeeOnlyTheirOwn(t *testing.T) {
	db := remoteSetup(t)
	addr := startListener(t, defaultListenOptions)
	alice, bob := dialRemote(t, addr), dialRemote(t, addr)

	aliceWorkflow := reviewWorkflow("review")
	aliceWorkflow.Filters = []string{"FilterNotDraft"}
	alice.register(registration("alice", "", aliceWorkflow))
	bob.register(registration("bob", "", reviewWorkflow("review")))

	for name, c := range map[string]struct {
		r       *remote
		filters []string
	}{"alice": {alice, []string{"FilterNotDraft"}}, "bob": {bob, nil}} {
		mine := &GetMyWorkflowsReply{}
		c.r.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, mine)
		if !mine.Known || mine.Login != name || len(mine.Workflows) != 1 || !slices.Equal(mine.Workflows[0].Filters, c.filters) {
			t.Errorf("%s's GetMyWorkflows = %+v", name, mine.MyWorkflowsPayload)
		}
	}

	seedSectionItem(t, db, "", "Needs Review", 7, time.Now())
	seedSectionItem(t, db, "alice", "Needs Review", aiNumber, time.Now())
	seedSectionItem(t, db, "bob", "Needs Review", 8, time.Now())
	for name, c := range map[string]struct {
		r    *remote
		want []string
	}{"alice": {alice, []string{fmt.Sprintf("Needs Review#%d", aiNumber)}}, "bob": {bob, []string{"Needs Review#8"}}} {
		reviews := &GetReviewsReply{}
		c.r.must("GetAllReviews", &GetReviewsArgs{}, reviews)
		if got := reviewNumbers(reviews.Items); !slices.Equal(got, c.want) {
			t.Errorf("%s's dashboard = %v, want %v", name, got, c.want)
		}
	}

	alice.must("AddComment", &AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 2, Body: "alice's draft"}, &AddCommentReply{})
	for name, c := range map[string]struct {
		r    *remote
		want []string
	}{"alice": {alice, []string{"alice's draft"}}, "bob": {bob, nil}} {
		pr := &GetPRReply{}
		c.r.must("GetPR", &GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, pr)
		if got := localDrafts(pr.PRPayload); !slices.Equal(got, c.want) {
			t.Errorf("%s sees drafts %v, want %v", name, got, c.want)
		}
	}
}

// The server's configuration and its users are an administrator's: the
// administrator token's session, or a user registered with AdminToken.
func TestRemoteAdminMethods(t *testing.T) {
	remoteSetup(t)
	useFakeGitHub(t) // RerunPlugins fetches in the background
	addr := startListener(t, defaultListenOptions)

	user := dialRemote(t, addr)
	user.register(registration("alice", ""))
	adminMethods := []struct {
		method string
		args   any
		reply  any
	}{
		{"GetConfig", &GetConfigArgs{}, &GetConfigReply{}},
		{"UpdateConfig", &UpdateConfigArgs{}, &UpdateConfigReply{}},
		{"RerunPlugins", &RerunPluginsArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &RerunPluginsReply{}},
		{"ListUsers", &ListUsersArgs{}, &ListUsersReply{}},
		{"DeleteUser", &DeleteUserArgs{Login: "nobody"}, &DeleteUserReply{}},
	}
	for _, m := range adminMethods {
		wantError(t, "a user's "+m.method, user.call(m.method, m.args, m.reply),
			"permission denied: RPCHandler."+m.method+" requires an administrator")
	}

	admin := dialRemote(t, addr)
	if reply := admin.authenticate(testAdminToken); !reply.Okay || !reply.Known || !reply.IsAdmin || reply.Login != "" {
		t.Fatalf("Authenticate with the administrator token = %+v", reply)
	}
	for _, m := range adminMethods {
		admin.must(m.method, m.args, m.reply)
	}
	users := &ListUsersReply{}
	admin.must("ListUsers", &ListUsersArgs{}, users)
	if len(users.Users) != 1 || users.Users[0].Login != "alice" || users.Users[0].WorkflowCount != 1 || users.Users[0].IsAdmin {
		t.Errorf("ListUsers = %+v", users.Users)
	}
	if _, err := time.Parse(time.RFC3339, users.Users[0].LastSeen); err != nil {
		t.Errorf("last_seen %q is not RFC 3339", users.Users[0].LastSeen)
	}
	// The administrator token's session is the server's own identity, which
	// has no stored workflows of its own.
	mine := &GetMyWorkflowsReply{}
	admin.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, mine)
	if mine.Okay || mine.Known {
		t.Errorf("GetMyWorkflows for the administrator token = %+v, want known=false", mine.MyWorkflowsPayload)
	}

	carol := dialRemote(t, addr)
	args := registration("carol", "")
	args.AdminToken = testAdminToken
	if reply := carol.register(args); !reply.IsAdmin {
		t.Errorf("registering with AdminToken: is_admin = false")
	}
	carol.must("ListUsers", &ListUsersArgs{}, &ListUsersReply{})
}

// SkipCache's refusal reaches a remote client as an RPC error.
func TestRemoteSkipCacheRefusal(t *testing.T) {
	remoteSetup(t)
	r := dialRemote(t, startListener(t, defaultListenOptions))
	r.register(registration("alice", ""))
	wantError(t, "GetPR", r.call("GetPR", &GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, SkipCache: true}, &GetPRReply{}),
		"permission denied: SkipCache requires an administrator")
	r.must("GetPR", &GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &GetPRReply{})
}

// A remote connection writes to GitHub under the server's token only where
// the server allows it, and a refused submit costs no drafts.
func TestRemoteWritesNeedServerIdentityActions(t *testing.T) {
	db := remoteSetup(t)
	fake := useFakeGitHub(t)
	addr := startListener(t, defaultListenOptions)
	alice := dialRemote(t, addr)
	// A workflow on another repo: a submit reprocesses the PR in the
	// background for every workflow that lists its repo, and one still
	// running when the test ends would outlive the fake GitHub.
	elsewhere := reviewWorkflow("review")
	elsewhere.Repos = []string{"acme/elsewhere"}
	alice.register(registration("alice", "", elsewhere))
	alice.must("AddComment", &AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 2, Body: "alice's draft"}, &AddCommentReply{})
	alice.must("AddComment", &AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 3}, &AddCommentReply{}) // empty: a submit deletes it

	submit := &SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT"}
	merge := &MergePRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}
	wantError(t, "SubmitReview", alice.call("SubmitReview", submit, &SubmitReviewReply{}), errServerIdentityRefused.Error())
	wantError(t, "MergePR", alice.call("MergePR", merge, &MergePRReply{}), errServerIdentityRefused.Error())
	if !strings.HasPrefix(errServerIdentityRefused.Error(), "refused: ") {
		t.Errorf("the refusal %q lacks the prefix clients match on", errServerIdentityRefused)
	}
	if drafts, _ := db.GetLocalCommentsForPR("alice", aiOwner, aiRepo, aiNumber); len(drafts) != 2 {
		t.Errorf("a refused submit left %d drafts, want both", len(drafts))
	}

	admin := dialRemote(t, addr)
	admin.authenticate(testAdminToken)
	wantError(t, "an administrator's MergePR", admin.call("MergePR", merge, &MergePRReply{}), errServerIdentityRefused.Error())

	fake.mu.Lock()
	if len(fake.reviews) != 0 || fake.merges != 0 {
		t.Errorf("GitHub received %d reviews and %d merges from refused writes", len(fake.reviews), fake.merges)
	}
	fake.mu.Unlock()

	// The local connection is whoever started the server, and never refused.
	local := &RPCHandler{}
	if err := local.MergePR(merge, &MergePRReply{}); err != nil {
		t.Errorf("the local MergePR: %v", err)
	}

	allowServerIdentityActions(t)
	alice.must("SubmitReview", submit, &SubmitReviewReply{})
	if drafts, _ := db.GetLocalCommentsForPR("alice", aiOwner, aiRepo, aiNumber); len(drafts) != 0 {
		t.Errorf("after submitting, alice still has drafts %v", drafts)
	}
	// Merging under the server's account is still an administrator's.
	wantError(t, "a user's MergePR", alice.call("MergePR", merge, &MergePRReply{}), errMergeAsServerNeedsAdmin.Error())
	merged := &MergePRReply{}
	admin.must("MergePR", merge, merged)
	if !merged.Merged {
		t.Errorf("an administrator's MergePR = %+v", merged)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.reviews) != 1 || fake.merges != 2 {
		t.Errorf("GitHub received %d reviews and %d merges, want 1 and 2 (the local one and the administrator's)", len(fake.reviews), fake.merges)
	}
}

// A user deleted while connected is refused as not authenticated, which sends
// their client back through registration, and comes back with the token
// their client holds.
func TestDeletedUserMustRegisterAgain(t *testing.T) {
	db := remoteSetup(t)
	addr := startListener(t, defaultListenOptions)
	alice := dialRemote(t, addr)
	token := alice.register(registration("alice", "")).Token
	alice.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{})

	admin := dialRemote(t, addr)
	admin.authenticate(testAdminToken)
	deleted := &DeleteUserReply{}
	admin.must("DeleteUser", &DeleteUserArgs{Login: "ALICE"}, deleted)
	if !deleted.Okay {
		t.Fatalf("DeleteUser = %+v", deleted)
	}
	notAuth := "not authenticated: RPCHandler.GetMyWorkflows needs an authenticated connection; call RPCHandler.Authenticate or RPCHandler.RegisterUser first"
	wantError(t, "after deletion", alice.call("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{}), notAuth)
	// The session was reset, not just refused once.
	wantError(t, "a second call after deletion", alice.call("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{}), notAuth)
	if reply := alice.authenticate(token); reply.Okay || reply.Known {
		t.Errorf("Authenticate with a deleted user's token = %+v, want known=false", reply)
	}

	again := alice.register(registration("alice", token))
	if !again.Created || again.Token != token {
		t.Errorf("re-registering after deletion = %+v, want created with the same token", again)
	}
	alice.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{})

	// Retention deletes the same way. Someone else then registering the login
	// does not hand them alice's open connection.
	if _, err := db.DeleteUsersIdleSince(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mallory := dialRemote(t, addr)
	if reply := mallory.register(registration("alice", "")); reply.Token == token {
		t.Fatal("a new registration of the login was handed the old token")
	}
	wantError(t, "after retention and someone else's registration", alice.call("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{}), notAuth)
	mallory.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{})
}

// Authenticating, registering and calls refresh last_seen, the calls at most
// once per touchInterval.
func TestRemoteActivityRefreshesLastSeen(t *testing.T) {
	db := remoteSetup(t)
	long := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	age := func(login string) {
		t.Helper()
		if err := db.TouchUser(login, long); err != nil {
			t.Fatal(err)
		}
	}
	lastSeen := func(login string) time.Time {
		t.Helper()
		u, err := db.GetUser(login)
		if err != nil {
			t.Fatal(err)
		}
		return u.LastSeen
	}
	recently := func(at time.Time) bool { return time.Since(at) < time.Minute }

	throttled := startListener(t, listenOptions{authTimeout: time.Minute, touchInterval: time.Hour})
	r := dialRemote(t, throttled)
	token := r.register(registration("alice", "")).Token
	if !recently(lastSeen("alice")) {
		t.Errorf("registering left last_seen at %v", lastSeen("alice"))
	}

	age("alice")
	r.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{})
	if got := lastSeen("alice"); !got.Equal(long) {
		t.Errorf("a call within touchInterval of signing in wrote last_seen %v", got)
	}

	r.authenticate(token)
	if !recently(lastSeen("alice")) {
		t.Errorf("authenticating left last_seen at %v", lastSeen("alice"))
	}

	every := startListener(t, listenOptions{authTimeout: time.Minute, touchInterval: 0})
	r = dialRemote(t, every)
	r.authenticate(token)
	age("alice")
	r.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{})
	if !recently(lastSeen("alice")) {
		t.Errorf("a call past touchInterval left last_seen at %v", lastSeen("alice"))
	}
}

// A connection that never authenticates is closed; one that does is kept.
func TestUnauthenticatedConnectionsAreClosed(t *testing.T) {
	remoteSetup(t)
	addr := startListener(t, listenOptions{authTimeout: 500 * time.Millisecond, touchInterval: time.Minute})
	idle, active := dialRemote(t, addr), dialRemote(t, addr)
	active.register(registration("alice", ""))

	time.Sleep(time.Second)
	if err := idle.call("Hello", &HelloArgs{}, &HelloReply{}); err == nil {
		t.Error("a connection that never authenticated is still served")
	}
	active.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{})
}

// Calls on one connection run concurrently while its requests are read one
// at a time; a refusal must reach its own call and no other. Run with -race.
func TestRefusalsStayWithTheirCalls(t *testing.T) {
	remoteSetup(t)
	r := dialRemote(t, startListener(t, defaultListenOptions))
	r.register(registration("alice", ""))

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				err := r.call("ListUsers", &ListUsersArgs{}, &ListUsersReply{})
				if err == nil || err.Error() != "permission denied: RPCHandler.ListUsers requires an administrator" {
					errs <- fmt.Sprintf("ListUsers: %v", err)
				}
				return
			}
			mine := &GetMyWorkflowsReply{}
			if err := r.call("GetMyWorkflows", &GetMyWorkflowsArgs{}, mine); err != nil || mine.Login != "alice" {
				errs <- fmt.Sprintf("GetMyWorkflows: %v (login %q)", err, mine.Login)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// Every exported RPCHandler method has a line in the method policy, and every
// line names a method: a new RPC has to be classified before it ships.
func TestMethodPolicyClassifiesEveryMethod(t *testing.T) {
	typ := reflect.TypeOf(&RPCHandler{})
	methods := map[string]bool{}
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		methods[name] = true
		if _, ok := methodPolicy[name]; !ok {
			t.Errorf("RPCHandler.%s is not in methodPolicy; classify it as public, user or admin", name)
		}
	}
	for name := range methodPolicy {
		if !methods[name] {
			t.Errorf("methodPolicy classifies %s, which RPCHandler does not have", name)
		}
	}

	for method, want := range map[string]access{
		"RPCHandler.Hello":     accessPublic,
		"RPCHandler.MergePR":   accessUser,
		"RPCHandler.GetConfig": accessAdmin,
		"RPCHandler.Unknown":   accessAdmin,
		"Other.Hello":          accessAdmin,
		"Hello":                accessAdmin,
	} {
		if got := methodAccess(method); got != want {
			t.Errorf("methodAccess(%q) = %v, want %v", method, got, want)
		}
	}
}

// The local connection is not gated at all.
func TestLocalConnectionIsNotGated(t *testing.T) {
	local := &RPCHandler{}
	for _, method := range []string{"RPCHandler.GetConfig", "RPCHandler.ListUsers", "RPCHandler.Unknown"} {
		if err := local.admit(method, time.Now()); err != nil {
			t.Errorf("local admit(%s) = %v", method, err)
		}
	}
	reply := &AuthenticateReply{}
	if err := local.Authenticate(&AuthenticateArgs{Token: testAdminToken}, reply); err != nil || reply.Okay {
		t.Errorf("Authenticate on the local connection = %+v (err %v), want okay=false", reply, err)
	}
}
