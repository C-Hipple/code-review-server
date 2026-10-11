package server

import (
	"crs/config"
	"crs/database"
	"errors"
	"fmt"
	"log/slog"
	"net/rpc"
	"strings"
	"time"
)

// The method policy: who a remote connection must be to call each RPC.
//
// Every exported RPCHandler method is classified here, in one table, and the
// table is enforced in one place — the codec every remote connection is served
// through (gatedCodec) — rather than by a check each handler has to remember.
// A method missing from the table is administrator-only, so a new RPC is
// closed to users until someone decides otherwise, and a test fails until
// that decision is written down (TestMethodPolicyClassifiesEveryMethod).
//
// The local stdio connection is not gated: it is whoever started the server.

// access is who may call a method.
type access int

const (
	// accessAdmin is the zero value, so an unclassified method is an
	// administrator's.
	accessAdmin access = iota
	// accessPublic methods may be called before authenticating: they are how a
	// connection authenticates.
	accessPublic
	// accessUser methods need an authenticated connection, and act as it.
	accessUser
)

// methodPolicy classifies every RPCHandler method by its bare name.
var methodPolicy = map[string]access{
	// Health check and authentication.
	"Hello":        accessPublic,
	"Authenticate": accessPublic,
	"RegisterUser": accessPublic,

	// A user's own dashboard, drafts, feedback and settings: everything reads
	// and writes as the viewer.
	"GetAllReviews":     accessUser,
	"GetPR":             accessUser, // SkipCache=true is an administrator's (checked in the handler)
	"GetAdjacentPR":     accessUser, // likewise
	"SyncPR":            accessUser,
	"AddComment":        accessUser,
	"EditComment":       accessUser,
	"DeleteComment":     accessUser,
	"SetFeedback":       accessUser,
	"RemovePRComments":  accessUser,
	"GetMyWorkflows":    accessUser,
	"UpdateMyWorkflows": accessUser,

	// Writes to GitHub. Which account they go out as, and whether they may at
	// all, is the write resolver's to say (writeClientFor): GitHub enforces a
	// user's own rights, and merging as the server's account is an
	// administrator's.
	"SubmitReview": accessUser,
	"MergePR":      accessUser,

	// Linking the user's own GitHub account, which their writes then go out
	// as. The administrator token's session, the server's own identity, has
	// no account to link and is answered okay=false.
	"StartGitHubLink": accessUser,
	"GetGitHubLink":   accessUser,
	"UnlinkGitHub":    accessUser,

	// Reads of what the server computes or caches for every user alike.
	// RunAIFeature's Force is the client's rerun button; what it spends is
	// bounded by the runner's concurrency cap.
	"ListPlugins":         accessUser,
	"GetPluginOutput":     accessUser,
	"ListAIFeatures":      accessUser,
	"RunAIFeature":        accessUser,
	"GetAIOutput":         accessUser,
	"GetRateLimitStatus":  accessUser,
	"GetRateLimitHistory": accessUser,
	"GetHunkContext":      accessUser, // reads the server's clones, at the PR's own commits
	"GetImage":            accessUser, // downloads only from GitHub's hosts

	// The server itself: its configuration, which holds plugin commands and
	// every key the shared token runs under; reruns that spend on every
	// plugin at once; and its users.
	"GetConfig":    accessAdmin,
	"UpdateConfig": accessAdmin,
	"RerunPlugins": accessAdmin,
	"ListUsers":    accessAdmin,
	"DeleteUser":   accessAdmin,
}

// handlerServicePrefix is how a request names an RPCHandler method.
const handlerServicePrefix = "RPCHandler."

// methodAccess is the access a request for serviceMethod needs. Anything not
// in the table, including methods of any other service, is an
// administrator's.
func methodAccess(serviceMethod string) access {
	name, ok := strings.CutPrefix(serviceMethod, handlerServicePrefix)
	if !ok {
		return accessAdmin
	}
	return methodPolicy[name]
}

// notAuthenticated is the refusal of a call made before the connection
// authenticated, or after its user was deleted. Clients match on the prefix
// and run their connect flow again.
func notAuthenticated(method string) error {
	return fmt.Errorf("not authenticated: %s needs an authenticated connection; call RPCHandler.Authenticate or RPCHandler.RegisterUser first", method)
}

// permissionDenied is the refusal of an administrator's method to a user.
func permissionDenied(method string) error {
	return fmt.Errorf("permission denied: %s requires an administrator", method)
}

// admit decides whether the connection may call serviceMethod now, and
// returns the refusal when it may not.
//
// A user's session is checked against their users row on every call, not just
// when it authenticated: an administrator's DeleteUser or the retention sweep
// can remove the user while the connection is open, and their next call must
// be refused as not authenticated — which sends the client back through
// registration — rather than served from a dashboard that no longer exists.
// The same read keeps the session's administrator flag current, and the call
// refreshes last_seen (at most once per touchInterval), which is what keeps
// an active user's workflows running and retention away.
func (h *RPCHandler) admit(serviceMethod string, now time.Time) error {
	if h.session == nil {
		return nil
	}
	level := methodAccess(serviceMethod)
	if level == accessPublic {
		return nil
	}
	st := h.session.snapshot()
	if !st.authenticated {
		return notAuthenticated(serviceMethod)
	}

	admin := st.admin
	// The administrator token's session is the server's own identity, login
	// "", and has no users row to check.
	if st.login != "" {
		db := config.C().DB
		u, err := db.GetUser(st.login)
		if errors.Is(err, database.ErrNotFound) || (err == nil && u.TokenHash != st.tokenHash) {
			h.session.signOutIf(st)
			slog.Info("A remote connection's user is no longer registered; it has to authenticate again", "login", st.login)
			return notAuthenticated(serviceMethod)
		}
		if err != nil {
			slog.Error("Error checking a remote connection's user", "login", st.login, "error", err)
			return fmt.Errorf("could not check this connection's user: %w", err)
		}
		admin = u.IsAdmin
		if admin != st.admin {
			h.session.setAdminIf(st, admin)
		}
		if h.session.touchDue(now) {
			if err := db.TouchUser(st.login, now); err != nil {
				slog.Warn("Error refreshing a user's last_seen", "login", st.login, "error", err)
			}
		}
	}

	if level == accessAdmin && !admin {
		return permissionDenied(serviceMethod)
	}
	return nil
}

// refusalServiceName and refusalMethod are where gatedCodec sends a request the
// policy refuses. The service is registered next to RPCHandler on every remote
// connection's rpc.Server; a client calling it directly is refused by the
// policy like any other unclassified method.
const (
	refusalServiceName = "Gate"
	refusalMethod      = refusalServiceName + ".Refuse"
)

// gatedCodec is a remote connection's codec with the method policy in front
// of it. A request the policy refuses is renamed to refusalMethod before
// net/rpc looks it up, so net/rpc never reaches the method asked for and
// answers with the refusal as the call's error, under the request's own id.
//
// net/rpc reads a connection's requests one at a time — each header, then its
// body — on one goroutine, and only then runs the call, concurrently with the
// next read. The refusal is carried from a header to its own body in refusal,
// which nothing but that read loop touches, and from there into the call's
// arguments (RefusalArgs). Nothing per request lives on the codec once its
// body is read, so a header read meanwhile cannot change the error a call
// already dispatched returns.
type gatedCodec struct {
	rpc.ServerCodec
	h       *RPCHandler
	refusal error
}

func newGatedCodec(inner rpc.ServerCodec, h *RPCHandler) *gatedCodec {
	return &gatedCodec{ServerCodec: inner, h: h}
}

func (c *gatedCodec) ReadRequestHeader(r *rpc.Request) error {
	c.refusal = nil
	if err := c.ServerCodec.ReadRequestHeader(r); err != nil {
		return err
	}
	if err := c.h.admit(r.ServiceMethod, time.Now()); err != nil {
		login, _, _ := c.h.session.Identity()
		slog.Info("Refused a remote call", "method", r.ServiceMethod, "login", login, "reason", err)
		c.refusal = err
		r.ServiceMethod = refusalMethod
	}
	return nil
}

func (c *gatedCodec) ReadRequestBody(body any) error {
	refusal := c.refusal
	c.refusal = nil
	if refusal == nil {
		return c.ServerCodec.ReadRequestBody(body)
	}
	// The arguments were for the refused method; nothing reads them.
	if err := c.ServerCodec.ReadRequestBody(nil); err != nil {
		return err
	}
	if args, ok := body.(*RefusalArgs); ok {
		args.err = refusal
	}
	return nil
}

// RefusalArgs carries a refused request's error to refusalService. Exported
// only because net/rpc requires it; it has nothing a client can set.
type RefusalArgs struct {
	err error
}

// RefusalReply is refusalService's empty reply; a refused call has only an
// error.
type RefusalReply struct{}

type refusalService struct{}

// Refuse fails with the refusal gatedCodec put in args.
func (refusalService) Refuse(args *RefusalArgs, reply *RefusalReply) error {
	if args.err == nil {
		return errors.New("rpc: can't find method " + refusalMethod)
	}
	return args.err
}
