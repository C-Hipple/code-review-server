package server

import (
	"crs/config"
	"crs/database"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/rpc"
	"reflect"
	"runtime/debug"
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
	// bounded by the runner's cap on the runs clients ask for
	// (ai.MaxRequestedRuns), past which it answers busy.
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

// refusalServiceName, refusalMethod and callMethod are where gatedCodec sends
// a remote connection's requests: a request the policy refuses to Refuse, and
// every RPCHandler request it admits to Call (gateService). The service is
// registered next to RPCHandler on every remote connection's rpc.Server; a
// client calling it directly is refused by the policy like any other
// unclassified method.
const (
	refusalServiceName = "Gate"
	refusalMethod      = refusalServiceName + ".Refuse"
	callMethod         = refusalServiceName + ".Call"
)

// gatedCodec is a remote connection's codec with the method policy in front
// of it. A request the policy refuses is renamed to refusalMethod before
// net/rpc looks it up, so net/rpc never reaches the method asked for and
// answers with the refusal as the call's error, under the request's own id.
// A request it admits for an RPCHandler method is renamed to callMethod,
// which runs that method under a recover.
//
// net/rpc reads a connection's requests one at a time — each header, then its
// body — on one goroutine, and only then runs the call, concurrently with the
// next read. The refusal or method is carried from a header to its own body in
// refusal and call, which nothing but that read loop touches, and from there
// into the call's arguments (RefusalArgs, CallArgs). Nothing per request lives
// on the codec once its body is read, so a header read meanwhile cannot change
// what a call already dispatched does.
type gatedCodec struct {
	rpc.ServerCodec
	h       *RPCHandler
	refusal error
	call    *handlerMethod
}

func newGatedCodec(inner rpc.ServerCodec, h *RPCHandler) *gatedCodec {
	return &gatedCodec{ServerCodec: inner, h: h}
}

func (c *gatedCodec) ReadRequestHeader(r *rpc.Request) error {
	c.refusal, c.call = nil, nil
	if err := c.ServerCodec.ReadRequestHeader(r); err != nil {
		return err
	}
	if err := c.h.admit(r.ServiceMethod, time.Now()); err != nil {
		login, _, _ := c.h.session.Identity()
		slog.Info("Refused a remote call", "method", r.ServiceMethod, "login", login, "reason", err)
		c.refusal = err
		r.ServiceMethod = refusalMethod
		return nil
	}
	if name, ok := strings.CutPrefix(r.ServiceMethod, handlerServicePrefix); ok {
		if m := handlerMethods[name]; m != nil {
			c.call = m
			r.ServiceMethod = callMethod
		}
	}
	return nil
}

func (c *gatedCodec) ReadRequestBody(body any) error {
	refusal, call := c.refusal, c.call
	c.refusal, c.call = nil, nil
	switch {
	case refusal != nil:
		// The arguments were for the refused method; nothing reads them.
		if err := c.ServerCodec.ReadRequestBody(nil); err != nil {
			return err
		}
		if args, ok := body.(*RefusalArgs); ok {
			args.err = refusal
		}
		return nil
	case call != nil:
		// Decoded as net/rpc would for the method itself.
		argv := reflect.New(call.argType)
		if err := c.ServerCodec.ReadRequestBody(argv.Interface()); err != nil {
			return err
		}
		if args, ok := body.(*CallArgs); ok {
			args.method, args.argv = call, argv
		}
		return nil
	}
	return c.ServerCodec.ReadRequestBody(body)
}

// handlerMethod is an RPCHandler method as net/rpc would serve it: exported,
// taking a pointer to its arguments and one to its reply, returning an error.
type handlerMethod struct {
	name      string
	fn        reflect.Value // the method's func, receiver first
	argType   reflect.Type  // what its arguments point to
	replyType reflect.Type  // what its reply points to
}

// handlerMethods are the RPCHandler methods gatedCodec routes through Call, by
// bare name.
var handlerMethods = func() map[string]*handlerMethod {
	methods := map[string]*handlerMethod{}
	t := reflect.TypeFor[*RPCHandler]()
	for i := range t.NumMethod() {
		m := t.Method(i)
		ft := m.Type
		if !m.IsExported() || ft.NumIn() != 3 || ft.NumOut() != 1 || ft.Out(0) != reflect.TypeFor[error]() ||
			ft.In(1).Kind() != reflect.Pointer || ft.In(2).Kind() != reflect.Pointer {
			continue
		}
		methods[m.Name] = &handlerMethod{name: m.Name, fn: m.Func, argType: ft.In(1).Elem(), replyType: ft.In(2).Elem()}
	}
	return methods
}()

// RefusalArgs carries a refused request's error to gateService.Refuse.
// Exported only because net/rpc requires it; it has nothing a client can set.
type RefusalArgs struct {
	err error
}

// RefusalReply is Refuse's empty reply; a refused call has only an error.
type RefusalReply struct{}

// CallArgs carries an admitted request's method and decoded arguments to
// gateService.Call. Exported only because net/rpc requires it; it has nothing
// a client can set.
type CallArgs struct {
	method *handlerMethod
	argv   reflect.Value
}

// CallReply is the reply of the method Call ran, and encodes as that reply,
// so the wire is what the method would have answered itself.
type CallReply struct {
	v any
}

func (r *CallReply) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.v)
}

// gateService is the service gatedCodec routes a remote connection's requests
// to, for the connection's own handler.
type gateService struct {
	h *RPCHandler
}

// Refuse fails with the refusal gatedCodec put in args.
func (gateService) Refuse(args *RefusalArgs, reply *RefusalReply) error {
	if args.err == nil {
		return errors.New("rpc: can't find method " + refusalMethod)
	}
	return args.err
}

// Call runs the RPCHandler method gatedCodec admitted, and turns a panic in it
// into the call's error. net/rpc runs every call on a goroutine of its own and
// recovers nothing, so a handler's panic would otherwise end the process: on a
// hosted server, every user's connections, link flows and writes in flight,
// for one bad request.
func (g gateService) Call(args *CallArgs, reply *CallReply) (err error) {
	m := args.method
	if m == nil {
		return errors.New("rpc: can't find method " + callMethod)
	}
	replyv := reflect.New(m.replyType)
	reply.v = replyv.Interface()
	defer func() {
		if p := recover(); p != nil {
			slog.Error("An RPC handler panicked", "method", handlerServicePrefix+m.name, "login", g.h.viewer(), "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %s%s failed; the server logged why", handlerServicePrefix, m.name)
		}
	}()
	out := m.fn.Call([]reflect.Value{reflect.ValueOf(g.h), args.argv, replyv})
	if e, _ := out[0].Interface().(error); e != nil {
		return e
	}
	return nil
}
