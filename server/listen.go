package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"time"
)

// Remote connections: crs --listen serves the same RPCHandler methods as the
// stdio server, over TCP, to clients on other machines. The wire is exactly
// stdio's — JSON-RPC 1.0, one JSON value after another — so a client frames it
// the same way. There is no TLS here: the server is meant for an internal
// network, behind a proxy that terminates TLS (docs/hosted_server.md).
//
// Each connection gets an rpc.Server, RPCHandler and Session of its own, so
// what one connection authenticates as never reaches another's calls, and is
// served through gatedCodec, which applies the method policy.

// listenOptions are the timings of a remote connection. Tests shorten them.
type listenOptions struct {
	// authTimeout is how long a connection may stay open without
	// authenticating. A client authenticates as soon as it connects; one that
	// does not is not a client of this server.
	authTimeout time.Duration
	// touchInterval is how often at most a connection's calls refresh its
	// user's last_seen.
	touchInterval time.Duration
}

var defaultListenOptions = listenOptions{
	authTimeout:   30 * time.Second,
	touchInterval: time.Minute,
}

// Listen serves remote clients on addr (host:port) until the listener fails.
// It does not return while serving.
func Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	slog.Info("Serving remote clients", "addr", ln.Addr().String())
	return serve(ln, defaultListenOptions)
}

// serve accepts connections on ln until it is closed, which ends serve without
// an error. Connections already open keep being served.
func serve(ln net.Listener, opts listenOptions) error {
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Most accept errors pass — running out of file descriptors, a
			// connection reset before it was accepted — so back off and keep
			// going rather than take every connected client down with the
			// listener.
			if backoff == 0 {
				backoff = 5 * time.Millisecond
			} else if backoff = 2 * backoff; backoff > time.Second {
				backoff = time.Second
			}
			slog.Error("Error accepting a connection", "error", err, "retry_in", backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		go serveConn(conn, opts)
	}
}

// serveConn serves one remote connection until the client closes it, or until
// authTimeout passes without the connection authenticating.
func serveConn(conn net.Conn, opts listenOptions) {
	remote := conn.RemoteAddr().String()
	session := &Session{touchInterval: opts.touchInterval}
	handler := &RPCHandler{session: session}
	server := rpc.NewServer()
	if err := server.Register(handler); err != nil {
		slog.Error("Error registering RPC handler", "error", err)
		conn.Close()
		return
	}
	if err := server.RegisterName(refusalServiceName, refusalService{}); err != nil {
		slog.Error("Error registering the refusal service", "error", err)
		conn.Close()
		return
	}

	slog.Info("Remote client connected", "remote", remote)
	timer := time.AfterFunc(opts.authTimeout, func() {
		if _, _, authenticated := session.Identity(); !authenticated {
			slog.Warn("Closing a connection that did not authenticate in time", "remote", remote, "timeout", opts.authTimeout)
			conn.Close()
		}
	})
	defer timer.Stop()

	// Returns once the client hangs up (or the connection is closed above),
	// after the calls in flight have answered, and closes the connection.
	server.ServeCodec(newGatedCodec(jsonrpc.NewServerCodec(conn), handler))

	login, admin, authenticated := session.Identity()
	slog.Info("Remote client disconnected", "remote", remote, "login", login, "admin", admin, "authenticated", authenticated)
}
