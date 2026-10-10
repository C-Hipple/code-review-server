// Command crs_native_host connects the Code Review Server Chrome extension
// (chrome_extension/) to codereviewserver. An extension can't start
// processes, so Chrome starts this native messaging host, and the host
// starts `codereviewserver --server` and translates between the two
// framings: Chrome's length-prefixed JSON messages on the host's stdin and
// stdout, and the server's newline-delimited JSON-RPC on its own. One
// connection from the extension is one host and one server; when Chrome
// closes the connection, the host closes the server's stdin and the server
// exits, as it does for every client.
//
// Responses too big for one native message (Chrome takes at most 1 MB from
// a host) arrive as a stream of crs_chunk messages, and the host reports its
// own state in crs_host events (messages.go). Nothing but native messaging
// frames is written to stdout: the host's log and the server's stderr go to
// $CRS_HOME/native_host.log.
//
// Chrome starts hosts with the desktop session's environment, not a login
// shell's, so the host first merges ~/.crs/native_host.env (or the file
// $CRS_NATIVE_HOST_ENV names) into its own; see env.go. install.sh registers
// the host with the browsers and can write that file.
//
// The host imports nothing from the module, so it builds without cgo and
// none of the server's dependencies.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
)

// version is the host's protocol version, reported in the ready event. It
// tracks the extension's version.
const version = "0.1.0"

// maxLogBytes bounds the log without rotating it: a log over this size is
// emptied when the next host starts.
const maxLogBytes = 5 << 20

func main() {
	os.Exit(realMain())
}

func realMain() int {
	// Chrome closing its end of stdout must surface as a write error, not
	// kill the host before it has stopped the server. Notify rather than
	// Ignore, because an ignored signal stays ignored in the server.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)

	home := homeDir()
	envPath := envFilePath(os.Getenv, home)
	var (
		envKeys, envWarnings []string
		envErr               error
	)
	if envPath != "" {
		envKeys, envWarnings, envErr = mergeEnvFile(envPath, home, os.Getenv(envFileVar) != "")
	}

	// The log location is read after the merge so the env file can set
	// CRS_HOME, as it does for the server.
	logPath := logFilePath(os.Getenv, home)
	var logOut io.Writer = os.Stderr // Chrome copies a host's stderr to its own
	logFile, logErr := openLog(logPath)
	if logErr == nil {
		defer logFile.Close()
		logOut = logFile
	}
	logger := log.New(logOut, fmt.Sprintf("native_host[%d] ", os.Getpid()), log.LstdFlags|log.Lmicroseconds)

	// Chrome passes the calling extension's origin first (and, on Windows, a
	// --parent-window handle); nothing else is expected.
	origin := "(none)"
	if len(os.Args) > 1 {
		origin = os.Args[1]
	}
	hostVersion := buildVersion()
	logger.Printf("crs_native_host %s starting for %s", hostVersion, origin)
	if logErr != nil {
		logger.Printf("logging to stderr: %v", logErr)
	}
	switch {
	case envErr != nil:
		logger.Print(envErr)
	case len(envKeys) > 0:
		logger.Printf("env file %s set %s", envPath, strings.Join(envKeys, ", "))
	}
	for _, w := range envWarnings {
		logger.Printf("env file %s: %s", envPath, w)
	}

	exe, err := os.Executable()
	if err != nil {
		logger.Printf("locating own executable: %v", err)
	}
	serverPath, err := resolveServer(os.Getenv, exe, home)
	if err != nil {
		msg := fmt.Sprintf("%v. Install it with `go install ./...` from the code-review-server repo, "+
			"or set %s (or PATH) in %s.", err, serverPathVar, envPath)
		logger.Print(msg)
		if frame, err := encodeMessage(hostMessage{Host: hostEvent{
			Event:   eventError,
			Message: msg,
			LogPath: logPath,
		}}); err == nil {
			_ = writeFrame(os.Stdout, frame)
		}
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Stdin, os.Stdout, hostConfig{
		ServerPath:   serverPath,
		Dir:          home,
		ServerStderr: logOut,
		Log:          logger,
		LogPath:      logPath,
		Version:      hostVersion,
	})
	logger.Printf("exiting with status %d", code)
	return code
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// logFilePath is $CRS_HOME/native_host.log, next to the server's own data.
func logFilePath(getenv func(string) string, home string) string {
	if crsHome := getenv("CRS_HOME"); crsHome != "" {
		return filepath.Join(crsHome, "native_host.log")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".crs", "native_host.log")
}

// openLog opens the log for appending, emptying it first when it has grown
// past maxLogBytes. It is private to the user: the server logs PR details.
func openLog(path string) (*os.File, error) {
	if path == "" {
		return nil, errors.New("no home directory and CRS_HOME is unset")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log: %w", err)
	}
	if info, err := f.Stat(); err == nil && info.Size() > maxLogBytes {
		_ = f.Truncate(0)
	}
	return f, nil
}

// buildVersion is version plus the commit the binary was built from, when
// the build recorded one, so a stale host is easy to spot in the log.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return version
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified {
		revision += ".dirty"
	}
	return version + "+" + revision
}
