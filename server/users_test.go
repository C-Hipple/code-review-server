package server

import (
	"crs/config"
	"crs/database"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// tryRegister calls RegisterUser over r and returns the reply, accepted or
// not.
func (r *remote) tryRegister(args *RegisterUserArgs) *RegisterUserReply {
	r.t.Helper()
	reply := &RegisterUserReply{}
	r.must("RegisterUser", args, reply)
	return reply
}

func storedWorkflowNames(t *testing.T, db *database.DB, login string) []string {
	t.Helper()
	rows, err := db.GetUserWorkflows(login)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names
}

func TestRegisterUser(t *testing.T) {
	db := remoteSetup(t)
	addr := startListener(t, defaultListenOptions)
	r := dialRemote(t, addr)

	refused := func(what string, args *RegisterUserArgs, wantMessage string) *RegisterUserReply {
		t.Helper()
		reply := r.tryRegister(args)
		if reply.Okay || reply.Token != "" || !strings.Contains(reply.Message, wantMessage) {
			t.Errorf("%s: reply = %+v, want refused with %q", what, reply, wantMessage)
		}
		return reply
	}

	wrongKey := registration("alice", "")
	wrongKey.RegistrationKey = "guess"
	refused("wrong key", wrongKey, "Wrong registration key")
	for _, login := range []string{"", "-alice", "alice-", "al--ice", "al ice", "al_ice", strings.Repeat("a", 40)} {
		refused(fmt.Sprintf("login %q", login), registration(login, ""), "is not a GitHub login")
	}
	t.Setenv(registrationKeyEnv, "")
	refused("without CRS_REGISTRATION_KEY", registration("alice", ""), "Registration is disabled")
	t.Setenv(registrationKeyEnv, testRegistrationKey)
	if users, _ := db.ListUsers(); len(users) != 0 {
		t.Fatalf("refused registrations created users: %+v", users)
	}
	// Still not signed in.
	if err := r.call("GetMyWorkflows", &GetMyWorkflowsArgs{}, &GetMyWorkflowsReply{}); err == nil {
		t.Error("a refused registration signed the connection in")
	}

	// Validation problems are reported field by field, and nothing is written.
	notifying := reviewWorkflow("review")
	yes := true
	notifying.DesktopNotifications = &yes
	bad := reviewWorkflow("bad")
	bad.WorkflowType = "NoSuchWorkflow"
	dup := reviewWorkflow("review")
	ctrl := reviewWorkflow("tab\there")
	reply := refused("invalid workflows", registration("alice", "", notifying, bad, dup, ctrl), "Not registered: found 3 problems")
	var fields []string
	for _, p := range reply.Errors {
		fields = append(fields, fmt.Sprintf("%d.%s", p.Workflow, p.Field))
	}
	if want := []string{"2.Name", "3.Name", "1.WorkflowType"}; !slices.Equal(fields, want) {
		t.Errorf("errors = %+v, want fields %v", reply.Errors, want)
	}
	many := make([]config.RawWorkflow, config.MaxUserWorkflows+1)
	for i := range many {
		many[i] = reviewWorkflow(fmt.Sprintf("wf-%d", i))
	}
	refused("too many workflows", registration("alice", "", many...), "Not registered")
	// A workflow with no Repos of its own needs the user's, or the server's.
	norepos := reviewWorkflow("review")
	norepos.Repos = nil
	refused("no repos to fall back on", registration("alice", "", norepos), "Not registered")
	if _, err := db.GetUser("alice"); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("an invalid registration created the user: %v", err)
	}

	// The first registration creates the user, with a fresh token.
	args := registration("Alice", "", notifying)
	args.Repos = []string{" acme/widgets ", ""}
	args.SectionPriority = map[string]int{"Needs Review": 1}
	created := r.tryRegister(args)
	if !created.Okay || !created.Created || created.Login != "Alice" || !tokenPattern.MatchString(created.Token) || created.IsAdmin {
		t.Fatalf("first registration = %+v", created)
	}
	u, err := db.GetUser("alice")
	if err != nil || u.TokenHash != hashToken(created.Token) || !slices.Equal(u.Repos, []string{"acme/widgets"}) {
		t.Fatalf("stored user = %+v (err %v)", u, err)
	}
	mine := &GetMyWorkflowsReply{}
	r.must("GetMyWorkflows", &GetMyWorkflowsArgs{}, mine)
	if len(mine.Workflows) != 1 || mine.Workflows[0].DesktopNotifications != nil || mine.Workflows[0].GithubUsername != "" {
		t.Errorf("stored workflows = %+v, want the one sent, without DesktopNotifications or a filled-in username", mine.Workflows)
	}
	if mine.SectionPriority["Needs Review"] != 1 || len(mine.WorkflowTypes) == 0 || len(mine.Filters) == 0 {
		t.Errorf("GetMyWorkflows = %+v", mine.MyWorkflowsPayload)
	}

	// Registering again with the token, in any case, replaces the workflows
	// and settings and hands back the same token.
	other := dialRemote(t, addr)
	again := other.tryRegister(registration("ALICE", created.Token, reviewWorkflow("second")))
	if !again.Okay || again.Created || again.Login != "Alice" || again.Token != created.Token {
		t.Errorf("re-registration = %+v", again)
	}
	if got := storedWorkflowNames(t, db, "alice"); !slices.Equal(got, []string{"second"}) {
		t.Errorf("after re-registering, workflows = %v", got)
	}

	// Without the token, or with another, the login is someone else's.
	for _, token := range []string{"", "crs_" + strings.Repeat("ab", 32), "nonsense"} {
		reply := dialRemote(t, addr).tryRegister(registration("alice", token, reviewWorkflow("hijack")))
		if reply.Okay || !strings.Contains(reply.Message, "login already registered") {
			t.Errorf("registering alice with token %q = %+v", token, reply)
		}
	}
	if got := storedWorkflowNames(t, db, "alice"); !slices.Equal(got, []string{"second"}) {
		t.Errorf("refused registrations changed alice's workflows to %v", got)
	}

	// A new login keeps a well-formed token the client holds — a user
	// deleted for inactivity comes back under it — and replaces anything else,
	// including a token another user holds.
	held := "crs_" + strings.Repeat("0123456789abcdef", 4)
	if reply := dialRemote(t, addr).tryRegister(registration("bob", held)); !reply.Okay || reply.Token != held {
		t.Errorf("bob with a well-formed token = %+v, want it kept", reply)
	}
	for login, token := range map[string]string{
		"carol": "crs_" + strings.Repeat("A", 64), // not lowercase
		"dave":  "crs_123",
		"erin":  held, // bob's
	} {
		reply := dialRemote(t, addr).tryRegister(registration(login, token))
		if !reply.Okay || !reply.Created || reply.Token == token || !tokenPattern.MatchString(reply.Token) {
			t.Errorf("%s with token %q = %+v, want a fresh token", login, token, reply)
		}
	}
}

// AdminToken makes a user an administrator, and leaving it out later does not
// take that away.
func TestRegisterUserAdminToken(t *testing.T) {
	db := remoteSetup(t)
	addr := startListener(t, defaultListenOptions)

	wrong := registration("alice", "")
	wrong.AdminToken = "guess"
	reply := dialRemote(t, addr).tryRegister(wrong)
	if !reply.Okay || reply.IsAdmin || !strings.Contains(reply.Message, "AdminToken did not match") {
		t.Errorf("a wrong AdminToken = %+v, want a registration without administrator rights that says so", reply)
	}

	promote := registration("alice", reply.Token)
	promote.AdminToken = testAdminToken
	if reply := dialRemote(t, addr).tryRegister(promote); !reply.Okay || !reply.IsAdmin {
		t.Errorf("re-registering with AdminToken = %+v", reply)
	}
	r := dialRemote(t, addr)
	if reply := r.tryRegister(registration("alice", reply.Token)); !reply.IsAdmin {
		t.Errorf("re-registering without AdminToken demoted the user: %+v", reply)
	}
	if u, _ := db.GetUser("alice"); !u.IsAdmin {
		t.Error("the stored user is no longer an administrator")
	}
	r.must("ListUsers", &ListUsersArgs{}, &ListUsersReply{})
}

func TestUpdateMyWorkflows(t *testing.T) {
	db := remoteSetup(t)
	addr := startListener(t, defaultListenOptions)
	r := dialRemote(t, addr)
	args := registration("alice", "", reviewWorkflow("review"))
	args.Repos = []string{"acme/widgets"}
	args.SectionSorting = map[string]string{"Needs Review": "oldest_first"}
	r.register(args)

	update := func(args *UpdateMyWorkflowsArgs) *UpdateMyWorkflowsReply {
		t.Helper()
		reply := &UpdateMyWorkflowsReply{}
		r.must("UpdateMyWorkflows", args, reply)
		return reply
	}

	if reply := update(&UpdateMyWorkflowsArgs{}); !reply.Okay || reply.Message != "No changes requested" || len(reply.Workflows) != 1 {
		t.Errorf("an empty update = %+v", reply)
	}

	// A workflow that falls back to the user's Repos...
	fallback := reviewWorkflow("fallback")
	fallback.Repos = nil
	wfs := []config.RawWorkflow{reviewWorkflow("review"), fallback}
	if reply := update(&UpdateMyWorkflowsArgs{Workflows: &wfs}); !reply.Okay || len(reply.Workflows) != 2 {
		t.Fatalf("replacing the workflows = %+v", reply)
	}
	// ...breaks if they are taken away while the server has none, and the
	// update is refused whole.
	none := []string{}
	sorting := map[string]string{}
	reply := update(&UpdateMyWorkflowsArgs{Repos: &none, SectionSorting: &sorting})
	if reply.Okay || len(reply.Errors) != 1 || reply.Errors[0].Workflow != 1 || reply.Errors[0].Field != "Repos" {
		t.Errorf("removing the Repos a workflow relies on = %+v", reply)
	}
	u, _ := db.GetUser("alice")
	if !slices.Equal(u.Repos, []string{"acme/widgets"}) || u.SectionSorting["Needs Review"] != "oldest_first" {
		t.Errorf("a refused update was written: %+v", u)
	}
	if !slices.Equal(reply.Repos, []string{"acme/widgets"}) {
		t.Errorf("a refused update replies with repos %v, want what is still stored", reply.Repos)
	}

	// Fields left out keep what is stored.
	priority := map[string]int{"Needs Review": 3}
	reply = update(&UpdateMyWorkflowsArgs{SectionPriority: &priority})
	if !reply.Okay || len(reply.Workflows) != 2 || !slices.Equal(reply.Repos, []string{"acme/widgets"}) ||
		reply.SectionPriority["Needs Review"] != 3 || reply.SectionSorting["Needs Review"] != "oldest_first" {
		t.Errorf("updating the priority alone = %+v", reply)
	}
	if got := storedWorkflowNames(t, db, "alice"); !slices.Equal(got, []string{"review", "fallback"}) {
		t.Errorf("stored workflows = %v", got)
	}

	// The administrator token's session has no workflows of its own.
	admin := dialRemote(t, addr)
	admin.authenticate(testAdminToken)
	adminReply := &UpdateMyWorkflowsReply{}
	admin.must("UpdateMyWorkflows", &UpdateMyWorkflowsArgs{SectionPriority: &priority}, adminReply)
	if adminReply.Okay || adminReply.Known {
		t.Errorf("UpdateMyWorkflows for the administrator token = %+v", adminReply)
	}
}

func TestDeleteUserReplies(t *testing.T) {
	remoteSetup(t)
	addr := startListener(t, defaultListenOptions)
	dialRemote(t, addr).register(registration("alice", ""))
	admin := dialRemote(t, addr)
	admin.authenticate(testAdminToken)

	for login, wantOkay := range map[string]bool{"": false, "nobody": false, " Alice ": true} {
		reply := &DeleteUserReply{}
		admin.must("DeleteUser", &DeleteUserArgs{Login: login}, reply)
		if reply.Okay != wantOkay || reply.Message == "" {
			t.Errorf("DeleteUser(%q) = %+v, want okay %v", login, reply, wantOkay)
		}
	}
	users := &ListUsersReply{}
	admin.must("ListUsers", &ListUsersArgs{}, users)
	if users.Users == nil || len(users.Users) != 0 {
		t.Errorf("ListUsers after deleting the only user = %#v, want []", users.Users)
	}
}

// Drafts and feedback written under a login after its user was deleted — a
// write admitted just before an administrator's DeleteUser lands just after
// it — are not handed to whoever registers the login next.
func TestRegistrationDropsWhatAPreviousHolderLeft(t *testing.T) {
	db := remoteSetup(t)
	draft := "the previous alice's draft"
	if _, err := db.InsertLocalComment("alice", aiOwner, aiRepo, aiNumber, "src/main.ts", 2, &draft, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFeedback("alice", aiOwner, aiRepo, aiNumber, &draft); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFeedback("bob", aiOwner, aiRepo, aiNumber, &draft); err != nil {
		t.Fatal(err)
	}

	r := dialRemote(t, startListener(t, defaultListenOptions))
	r.register(registration("Alice", ""))
	if n := draftCount(t, db, "alice"); n != 0 {
		t.Errorf("the new alice inherited %d drafts", n)
	}
	if feedback, _ := db.GetFeedback("alice", aiOwner, aiRepo, aiNumber); feedback != "" {
		t.Errorf("the new alice inherited feedback %q", feedback)
	}
	if feedback, _ := db.GetFeedback("bob", aiOwner, aiRepo, aiNumber); feedback != draft {
		t.Errorf("bob's feedback = %q; registering alice touched it", feedback)
	}
}
