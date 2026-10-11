package server

import (
	"crs/config"
	"crs/database"
	"crs/workflows"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"
)

// --- Users ---
//
// A hosted server (crs --listen) keeps a dashboard for each registered user.
// A client registers with RegisterUser, sending the workflows and section
// settings from its own config file, and gets back a bearer token; from then on
// it opens each connection with Authenticate. Registration is by claim — the
// server takes the login it is given — and gated by a shared key, so it is for
// a team's internal network rather than the open internet.
//
// Two secrets come from the environment, never the config file:
// CRS_REGISTRATION_KEY, without which nobody can register, and
// CRS_ADMIN_TOKEN, which authenticates as the server's own identity (login "",
// an administrator) and, sent as RegisterUser's AdminToken, makes a user an
// administrator.

const (
	registrationKeyEnv = "CRS_REGISTRATION_KEY"
	adminTokenEnv      = "CRS_ADMIN_TOKEN"
)

// tokenPattern is the form of every token the server issues: "crs_" and 32
// random bytes in lowercase hex.
var tokenPattern = regexp.MustCompile(`^crs_[0-9a-f]{64}$`)

// githubLoginPattern is a GitHub login: letters, digits and single hyphens,
// neither first nor last. GitHub also caps it at 39 characters.
var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

const maxGitHubLoginLength = 39

func validGitHubLogin(login string) bool {
	return len(login) <= maxGitHubLoginLength && githubLoginPattern.MatchString(login)
}

// newToken mints a bearer token.
func newToken() string {
	b := make([]byte, 32)
	// crypto/rand.Read never fails; it crashes the program rather than return
	// short.
	_, _ = rand.Read(b)
	return "crs_" + hex.EncodeToString(b)
}

// hashToken is what the users table holds in place of a token. A token is 256
// random bits, so an unsalted hash is as good as a slow one: there is nothing
// to guess.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// secretMatches compares a client's secret with the server's in constant
// time. Both are hashed first so that not even their lengths can be timed.
func secretMatches(given, want string) bool {
	a, b := sha256.Sum256([]byte(given)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// isAdminToken reports whether token is the server's administrator token.
// With CRS_ADMIN_TOKEN unset there is none, and nothing matches.
func isAdminToken(token string) bool {
	want := os.Getenv(adminTokenEnv)
	return want != "" && secretMatches(token, want)
}

// githubLinkFields are the reply fields that say whether the user's own GitHub
// account is linked for writes (github_link.go): GitHubLinked when a link is
// stored that the server can use, GitHubLinkAvailable when the server can
// link one. Both are false for the server's own identity, which writes with
// the server's token. githubLinkFieldsFor fills them.
type githubLinkFields struct {
	GitHubLinked        bool `json:"github_linked"`
	GitHubLinkAvailable bool `json:"github_link_available"`
}

type AuthenticateArgs struct {
	Token string `json:"Token"`
}

type AuthenticateReply struct {
	Okay bool `json:"okay"`
	// Known is false when the token belongs to no user: never registered, or
	// deleted since. The client registers (again) on that.
	Known   bool   `json:"known"`
	Login   string `json:"login"`
	IsAdmin bool   `json:"is_admin"`
	Message string `json:"message"`
	githubLinkFields
}

// Authenticate signs the connection in with a token RegisterUser issued, or
// with the administrator token, which acts as the server's own identity (login
// ""). A token that matches nothing leaves the connection as it was.
func (h *RPCHandler) Authenticate(args *AuthenticateArgs, reply *AuthenticateReply) error {
	if h.session == nil {
		reply.Known = true
		reply.Message = "This is the local connection: it acts as the server's own identity and needs no token"
		return nil
	}
	token := strings.TrimSpace(args.Token)
	if token == "" {
		reply.Message = "No token given; register with RegisterUser to get one"
		return nil
	}
	now := time.Now()
	if isAdminToken(token) {
		h.session.signIn("", "", true, now)
		reply.Okay, reply.Known, reply.IsAdmin = true, true, true
		reply.Message = "Authenticated as the server's administrator"
		slog.Info("A remote connection authenticated with the administrator token")
		return nil
	}

	hash := hashToken(token)
	db := config.C().DB
	u, err := db.GetUserByTokenHash(hash)
	if errors.Is(err, database.ErrNotFound) {
		reply.Message = "No user holds this token: it was never registered here, or its user was deleted after a stretch of inactivity; register again with RegisterUser"
		return nil
	}
	if err != nil {
		slog.Error("Error looking up a token", "error", err)
		return err
	}
	if err := db.TouchUser(u.Login, now); err != nil {
		slog.Warn("Error refreshing a user's last_seen", "login", u.Login, "error", err)
	}
	h.session.signIn(u.Login, hash, u.IsAdmin, now)
	reply.Okay, reply.Known = true, true
	reply.Login, reply.IsAdmin = u.Login, u.IsAdmin
	reply.githubLinkFields = githubLinkFieldsFor(u.Login)
	reply.Message = fmt.Sprintf("Authenticated as %s", u.Login)
	slog.Info("A remote connection authenticated", "login", u.Login, "admin", u.IsAdmin)
	return nil
}

type RegisterUserArgs struct {
	RegistrationKey string `json:"RegistrationKey"`
	Login           string `json:"Login"`
	// Token is the token the client holds, if any. Re-registering a known login
	// must present its token; a new login reuses a well-formed one, so a client
	// whose user was deleted gets the token it already stores back.
	Token           string               `json:"Token"`
	Workflows       []config.RawWorkflow `json:"Workflows"`
	Repos           []string             `json:"Repos"`
	SectionPriority map[string]int       `json:"SectionPriority"`
	SectionSorting  map[string]string    `json:"SectionSorting"`
	// AdminToken, when it is the server's CRS_ADMIN_TOKEN, makes the user an
	// administrator.
	AdminToken string `json:"AdminToken"`
}

type RegisterUserReply struct {
	Okay    bool   `json:"okay"`
	Created bool   `json:"created"`
	Login   string `json:"login"`
	// Token is the user's bearer token. The server keeps only its hash, so
	// this reply is the only place it is ever shown.
	Token   string                   `json:"token"`
	IsAdmin bool                     `json:"is_admin"`
	Message string                   `json:"message"`
	Errors  []config.ValidationError `json:"errors"`
	githubLinkFields
}

// RegisterUser registers a login with the workflows and settings from the
// client's config file, or replaces them for a login already registered with
// the same token, and signs the connection in as that user. A rejected
// registration — wrong key, someone else's login, settings that do not
// validate — writes nothing and is reported as okay=false, not as an RPC
// error, with each validation problem in errors.
func (h *RPCHandler) RegisterUser(args *RegisterUserArgs, reply *RegisterUserReply) error {
	reply.Errors = []config.ValidationError{}
	if h.session == nil {
		reply.Message = "This is the local connection: it acts as the server's own identity, and its workflows are the config file's"
		return nil
	}
	key := os.Getenv(registrationKeyEnv)
	if key == "" {
		reply.Message = fmt.Sprintf("Registration is disabled on this server: %s is not set", registrationKeyEnv)
		return nil
	}
	if !secretMatches(args.RegistrationKey, key) {
		reply.Message = "Wrong registration key"
		return nil
	}
	login := strings.TrimSpace(args.Login)
	if !validGitHubLogin(login) {
		reply.Message = fmt.Sprintf("%q is not a GitHub login: letters, digits and single hyphens, at most %d characters", login, maxGitHubLoginLength)
		return nil
	}

	db := config.C().DB
	existing, err := db.GetUser(login)
	if err != nil && !errors.Is(err, database.ErrNotFound) {
		slog.Error("Error looking up a user to register", "login", login, "error", err)
		return err
	}
	token := strings.TrimSpace(args.Token)
	if existing != nil && !secretMatches(hashToken(token), existing.TokenHash) {
		reply.Message = loginTakenMessage(existing.Login)
		return nil
	}

	raws := normalizeUserWorkflows(args.Workflows)
	repos := trimList(args.Repos)
	if problems := validateUserSettings(login, raws, repos, args.SectionSorting); len(problems) > 0 {
		reply.Errors = problems
		reply.Message = fmt.Sprintf("Not registered: found %s", pluralize(len(problems), "problem", "problems"))
		return nil
	}
	rows, err := encodeUserWorkflows(raws)
	if err != nil {
		return err
	}

	adminTokenMatches := isAdminToken(args.AdminToken)
	user := database.User{
		Login:           login,
		SectionPriority: args.SectionPriority,
		SectionSorting:  args.SectionSorting,
		Repos:           repos,
		IsAdmin:         adminTokenMatches,
	}
	now := time.Now()
	if existing != nil {
		// Same token, so the same user: replace what they had. An
		// administrator stays one whether or not this registration says so.
		user.Login = existing.Login
		user.TokenHash = existing.TokenHash
		user.IsAdmin = existing.IsAdmin || adminTokenMatches
		if err := db.UpdateUserWithWorkflows(user, rows); err != nil {
			slog.Error("Error updating a registered user", "login", user.Login, "error", err)
			return err
		}
		if err := db.TouchUser(user.Login, now); err != nil {
			slog.Warn("Error refreshing a user's last_seen", "login", user.Login, "error", err)
		}
	} else {
		// A token the client already holds is reused, so a user deleted for
		// inactivity comes back under the token their client stores — unless
		// it is not one this server could have issued, or another user holds
		// it.
		if !tokenPattern.MatchString(token) {
			token = newToken()
		} else if _, err := db.GetUserByTokenHash(hashToken(token)); err == nil {
			token = newToken()
		}
		user.TokenHash = hashToken(token)
		user.CreatedAt, user.LastSeen = now, now
		if err := db.CreateUserWithWorkflows(user, rows); err != nil {
			// Most likely the same login registered by another connection
			// since the lookup above.
			if _, lookupErr := db.GetUser(login); lookupErr == nil {
				reply.Message = loginTakenMessage(login)
				return nil
			}
			slog.Error("Error registering a user", "login", login, "error", err)
			return err
		}
		reply.Created = true
	}

	h.session.signIn(user.Login, user.TokenHash, user.IsAdmin, now)
	reply.Okay = true
	reply.Login = user.Login
	reply.Token = token
	reply.IsAdmin = user.IsAdmin
	reply.githubLinkFields = githubLinkFieldsFor(user.Login)
	verb := "Registered"
	if !reply.Created {
		verb = "Updated the registration of"
	}
	reply.Message = fmt.Sprintf("%s %s with %s; the dashboard follows on the next workflow cycle",
		verb, user.Login, pluralize(len(rows), "workflow", "workflows"))
	if args.AdminToken != "" && !adminTokenMatches {
		reply.Message += "; AdminToken did not match, so not as an administrator"
	}
	slog.Info("Registered a user", "login", user.Login, "created", reply.Created, "admin", user.IsAdmin, "workflows", len(rows))
	return nil
}

func loginTakenMessage(login string) string {
	return fmt.Sprintf("login already registered: %s is registered with another token; register with that token, or ask an administrator to delete the user", login)
}

// normalizeUserWorkflows trims a user's workflows as UpdateConfig trims the
// server's, and drops DesktopNotifications: a user's workflow never notifies
// (the popup would land on the server's machine), so the stored workflow does
// not pretend to.
func normalizeUserWorkflows(raws []config.RawWorkflow) []config.RawWorkflow {
	cleaned := *normalizeWorkflows(&raws)
	for i := range cleaned {
		cleaned[i].DesktopNotifications = nil
	}
	return cleaned
}

// validateUserSettings checks a user's workflows and settings by the rules
// UpdateConfig applies to the server's own (see config.ValidateUserSettings),
// with each workflow seen as it will run: login's, with an empty
// GithubUsername standing for login, and falling back to the user's Repos —
// or, when they have none, the server's.
func validateUserSettings(login string, raws []config.RawWorkflow, repos []string, sorting map[string]string) []config.ValidationError {
	problems := config.ValidateUserSettings(raws, repos, sorting)
	fallback := repos
	if len(fallback) == 0 {
		fallback = config.C().Repos
	}
	asRun := make([]config.RawWorkflow, len(raws))
	for i, raw := range raws {
		asRun[i] = workflows.AsUserWorkflow(login, raw)
	}
	return append(problems, workflows.ValidateWorkflows(asRun, fallback)...)
}

// encodeUserWorkflows turns validated workflows into the rows the database
// keeps: each one exactly as the user sent it, as the JSON of a RawWorkflow.
func encodeUserWorkflows(raws []config.RawWorkflow) ([]database.UserWorkflow, error) {
	rows := make([]database.UserWorkflow, 0, len(raws))
	for i, raw := range raws {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		rows = append(rows, database.UserWorkflow{Position: i, Name: raw.Name, JSON: string(b)})
	}
	return rows, nil
}

// MyWorkflowsPayload is the shared body of GetMyWorkflows and
// UpdateMyWorkflows: the connection's user's stored workflows and settings,
// plus the registries a client builds its pickers from, as GetConfig has them.
type MyWorkflowsPayload struct {
	Okay bool `json:"okay"`
	// Known is false when the connection's login has no users row: the
	// administrator token's session, whose workflows are the config file's,
	// or a user deleted a moment ago.
	Known           bool                         `json:"known"`
	Login           string                       `json:"login"`
	Workflows       []config.RawWorkflow         `json:"workflows"`
	Repos           []string                     `json:"repos"`
	SectionPriority map[string]int               `json:"section_priority"`
	SectionSorting  map[string]string            `json:"section_sorting"`
	WorkflowTypes   []workflows.WorkflowTypeInfo `json:"workflow_types"`
	Filters         []workflows.FilterInfo       `json:"filters"`
	githubLinkFields
}

// populate fills the payload with login's stored state, workflows as they were
// sent (DecodeUserWorkflow, not AsUserWorkflow: nothing filled in). Lists and
// maps are empty rather than null.
func (p *MyWorkflowsPayload) populate(login string) error {
	p.Login = login
	p.Workflows = []config.RawWorkflow{}
	p.Repos = []string{}
	p.SectionPriority = map[string]int{}
	p.SectionSorting = map[string]string{}
	p.WorkflowTypes = workflows.WorkflowTypes()
	p.Filters = workflows.FilterTypes()

	u, err := lookupUser(login)
	if err != nil || u == nil {
		return err
	}
	stored, err := config.C().DB.GetUserWorkflows(u.Login)
	if err != nil {
		return err
	}
	for _, row := range stored {
		raw, err := workflows.DecodeUserWorkflow(row)
		if err != nil {
			slog.Warn("A stored user workflow does not decode", "user", u.Login, "workflow", row.Name, "error", err)
		}
		p.Workflows = append(p.Workflows, raw)
	}
	p.Okay, p.Known = true, true
	p.Login = u.Login
	p.Repos = u.Repos
	p.SectionPriority = u.SectionPriority
	p.SectionSorting = u.SectionSorting
	p.githubLinkFields = githubLinkFieldsFor(u.Login)
	return nil
}

// lookupUser returns login's users row, or nil when there is none — including
// for "", the server's own identity, which never has one.
func lookupUser(login string) (*database.User, error) {
	if login == "" {
		return nil, nil
	}
	u, err := config.C().DB.GetUser(login)
	if errors.Is(err, database.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

type GetMyWorkflowsArgs struct{}

type GetMyWorkflowsReply struct {
	MyWorkflowsPayload
}

// GetMyWorkflows returns the connection's user's workflows and settings, as
// they were registered or last updated.
func (h *RPCHandler) GetMyWorkflows(args *GetMyWorkflowsArgs, reply *GetMyWorkflowsReply) error {
	return reply.populate(h.viewer())
}

// UpdateMyWorkflowsArgs is a partial update, like UpdateConfigArgs: a field
// left out (null) keeps what is stored, and sending Workflows replaces the
// whole list.
type UpdateMyWorkflowsArgs struct {
	Workflows       *[]config.RawWorkflow `json:"Workflows"`
	Repos           *[]string             `json:"Repos"`
	SectionPriority *map[string]int       `json:"SectionPriority"`
	SectionSorting  *map[string]string    `json:"SectionSorting"`
}

type UpdateMyWorkflowsReply struct {
	MyWorkflowsPayload
	Message string                   `json:"message"`
	Errors  []config.ValidationError `json:"errors"`
}

// UpdateMyWorkflows changes the connection's user's workflows and settings.
// The result is validated whole, as RegisterUser validates it — a change to
// Repos can break a stored workflow that falls back to them — and nothing is
// written unless it passes; a rejected update is okay=false with errors, and
// the reply then shows what is still stored.
func (h *RPCHandler) UpdateMyWorkflows(args *UpdateMyWorkflowsArgs, reply *UpdateMyWorkflowsReply) error {
	reply.Errors = []config.ValidationError{}
	login := h.viewer()
	u, err := lookupUser(login)
	if err != nil {
		return err
	}
	if u == nil {
		if err := reply.populate(login); err != nil {
			return err
		}
		reply.Message = "This connection has no registered user to update; register with RegisterUser"
		return nil
	}
	if args.Workflows == nil && args.Repos == nil && args.SectionPriority == nil && args.SectionSorting == nil {
		if err := reply.populate(u.Login); err != nil {
			return err
		}
		reply.Message = "No changes requested"
		return nil
	}

	var raws []config.RawWorkflow
	var rows []database.UserWorkflow
	if args.Workflows != nil {
		raws = normalizeUserWorkflows(*args.Workflows)
		if rows, err = encodeUserWorkflows(raws); err != nil {
			return err
		}
	} else {
		// Kept as stored, row for row; decoded only to validate them against
		// the settings that change.
		if rows, err = config.C().DB.GetUserWorkflows(u.Login); err != nil {
			return err
		}
		for _, row := range rows {
			raw, _ := workflows.DecodeUserWorkflow(row)
			raws = append(raws, raw)
		}
	}
	if args.Repos != nil {
		u.Repos = trimList(*args.Repos)
	}
	if args.SectionPriority != nil {
		u.SectionPriority = *args.SectionPriority
	}
	if args.SectionSorting != nil {
		u.SectionSorting = *args.SectionSorting
	}

	if problems := validateUserSettings(u.Login, raws, u.Repos, u.SectionSorting); len(problems) > 0 {
		if err := reply.populate(u.Login); err != nil {
			return err
		}
		reply.Okay = false
		reply.Errors = problems
		reply.Message = fmt.Sprintf("Not saved: found %s", pluralize(len(problems), "problem", "problems"))
		return nil
	}
	if err := config.C().DB.UpdateUserWithWorkflows(*u, rows); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return notAuthenticated(handlerServicePrefix + "UpdateMyWorkflows")
		}
		slog.Error("Error saving a user's workflows", "login", u.Login, "error", err)
		return err
	}
	if err := reply.populate(u.Login); err != nil {
		return err
	}
	reply.Message = "Saved; the dashboard follows on the next workflow cycle"
	slog.Info("Updated a user's workflows", "login", u.Login, "workflows", len(rows))
	return nil
}

type ListUsersArgs struct{}

// UserInfo is one registered user as an administrator sees them. Never their
// bearer token, which the server does not have, nor their GitHub token, which
// it keeps sealed: only whether one is linked, and to which account.
type UserInfo struct {
	Login         string `json:"login"`
	IsAdmin       bool   `json:"is_admin"`
	CreatedAt     string `json:"created_at"` // RFC 3339
	LastSeen      string `json:"last_seen"`  // RFC 3339
	WorkflowCount int    `json:"workflow_count"`
	GitHubLinked  bool   `json:"github_linked"`
	GitHubLogin   string `json:"github_login"`
}

type ListUsersReply struct {
	Users []UserInfo `json:"users"`
}

// ListUsers lists every registered user, by login.
func (h *RPCHandler) ListUsers(args *ListUsersArgs, reply *ListUsersReply) error {
	db := config.C().DB
	users, err := db.ListUsers()
	if err != nil {
		return err
	}
	stored, err := db.GetAllUserWorkflows()
	if err != nil {
		return err
	}
	setup := currentLinkSetup()
	reply.Users = make([]UserInfo, 0, len(users))
	for _, u := range users {
		info := UserInfo{
			Login:         u.Login,
			IsAdmin:       u.IsAdmin,
			CreatedAt:     u.CreatedAt.Format(time.RFC3339),
			LastSeen:      u.LastSeen.Format(time.RFC3339),
			WorkflowCount: len(stored[u.Login]),
		}
		if link, err := loadLink(u.Login, setup); err != nil {
			slog.Warn("Error reading a user's GitHub link", "login", u.Login, "error", err)
		} else if link.cred != nil {
			info.GitHubLinked, info.GitHubLogin = true, link.cred.GitHubLogin
		}
		reply.Users = append(reply.Users, info)
	}
	return nil
}

type DeleteUserArgs struct {
	Login string `json:"Login"`
}

type DeleteUserReply struct {
	Okay    bool   `json:"okay"`
	Message string `json:"message"`
}

// DeleteUser deletes a user with their workflows, drafts and feedback, as the
// retention sweep would, cancels a GitHub link they have in progress, and
// revokes the token of one they linked (best effort, with the client
// secret). Their dashboard empties on the next workflow cycle, and a
// connection still signed in as them is refused as not authenticated on its
// next call, so its client registers again — unless the registration key has
// changed meanwhile.
func (h *RPCHandler) DeleteUser(args *DeleteUserArgs, reply *DeleteUserReply) error {
	login := strings.TrimSpace(args.Login)
	if login == "" {
		reply.Message = "Login is required"
		return nil
	}
	// Waited for, so a link completing now cannot outlive the user unrevoked.
	githubLinks.stop(login, true)
	deleted, err := config.C().DB.DeleteUserWithCredential(login)
	if errors.Is(err, database.ErrNotFound) {
		reply.Message = fmt.Sprintf("No user named %q is registered", login)
		return nil
	}
	if err != nil {
		slog.Error("Error deleting a user", "login", login, "error", err)
		return err
	}
	reply.Okay = true
	reply.Message = fmt.Sprintf("Deleted %s with their workflows, drafts and feedback; their dashboard empties on the next workflow cycle", deleted.Login)
	if deleted.GitHubCredential != "" {
		revokeErr := revokeSealed(currentLinkSetup(), deleted.Login, deleted.GitHubCredential, "user deleted")
		reply.Message += revokeOutcome(revokeErr, "their linked GitHub")
	}
	slog.Info("Deleted a user", "login", deleted.Login, "by", h.viewer())
	return nil
}
