package ghauth_test

import (
	"bytes"
	"context"
	"crs/ghauth"
	"crs/ghauth/ghauthtest"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// startFlow starts a device flow on the fake and polls it to its end.
func startFlow(t *testing.T, app *ghauth.App) (*ghauth.Token, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dc, err := app.RequestDeviceCode(ctx)
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	return app.PollToken(ctx, dc)
}

func TestDeviceFlowGrantsAToken(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	gh.Script("authorization_pending", "authorization_pending", "ok")
	app := gh.App()
	app.Scopes = "repo, read:org"

	ctx := context.Background()
	before := time.Now()
	dc, err := app.RequestDeviceCode(ctx)
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if dc.UserCode != ghauthtest.UserCode || dc.VerificationURI != ghauthtest.VerificationURI || dc.Interval != 1 {
		t.Errorf("device code = %+v", dc)
	}
	// expires_in is 900 of the fake's seconds.
	if want := before.Add(900 * ghauthtest.Second); dc.ExpiresAt.Before(want) || dc.ExpiresAt.After(want.Add(time.Second)) {
		t.Errorf("ExpiresAt = %v, want about %v", dc.ExpiresAt, want)
	}
	if got := gh.Scopes(); len(got) != 1 || got[0] != "repo read:org" {
		t.Errorf("scopes asked for = %q, want them space-separated", got)
	}

	tok, err := app.PollToken(ctx, dc)
	if err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	if !strings.HasPrefix(tok.AccessToken, "gho_") || tok.TokenType != "bearer" || tok.Scope != "repo" {
		t.Errorf("token = %+v", tok.AccessToken)
	}
	if !tok.ExpiresAt.IsZero() || tok.RefreshToken != "" {
		t.Errorf("an OAuth App's token expires at %v with a refresh token %v", tok.ExpiresAt, tok.RefreshToken != "")
	}
	if polls := gh.Polls(); len(polls) != 3 {
		t.Errorf("polled %d times, want 3", len(polls))
	}

	login, err := app.Login(ctx, tok.AccessToken)
	if err != nil || login != "octocat" {
		t.Errorf("Login = %q, %v", login, err)
	}
}

// slow_down adds five seconds to the interval, and the next polls keep to it.
func TestDeviceFlowSlowsDown(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	gh.Script("authorization_pending", "slow_down", "authorization_pending", "ok")
	if _, err := startFlow(t, gh.App()); err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	polls := gh.Polls()
	if len(polls) != 4 {
		t.Fatalf("polled %d times, want 4", len(polls))
	}
	// One second apart, then six.
	for i, minGap := range []time.Duration{ghauthtest.Second, 6 * ghauthtest.Second, 6 * ghauthtest.Second} {
		// The timer sleeps at least the interval; leave a little for the
		// requests' own jitter.
		if gap := polls[i+1].Sub(polls[i]); gap < minGap-ghauthtest.Second/2 {
			t.Errorf("poll %d came %v after the one before, want at least %v", i+1, gap, minGap)
		}
	}
}

func TestDeviceFlowEnds(t *testing.T) {
	for _, tc := range []struct {
		script []string
		want   error
	}{
		{[]string{"access_denied"}, ghauth.ErrDenied},
		{[]string{"authorization_pending", "expired_token"}, ghauth.ErrExpired},
	} {
		gh := ghauthtest.NewServer(t)
		gh.Script(tc.script...)
		if _, err := startFlow(t, gh.App()); !errors.Is(err, tc.want) {
			t.Errorf("after %v: err = %v, want %v", tc.script, err, tc.want)
		}
	}

	// GitHub refusing the app outright ends the flow with its error code.
	gh := ghauthtest.NewServer(t)
	gh.Script("device_flow_disabled")
	_, err := startFlow(t, gh.App())
	var apiErr *ghauth.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "device_flow_disabled" {
		t.Errorf("device_flow_disabled: err = %v", err)
	}

	// A client ID GitHub does not know.
	app := gh.App()
	app.ClientID = "unknown"
	if _, err := app.RequestDeviceCode(context.Background()); !errors.As(err, &apiErr) {
		t.Errorf("RequestDeviceCode with an unknown client: err = %v", err)
	}
	app.ClientID = ""
	if _, err := app.RequestDeviceCode(context.Background()); !errors.Is(err, ghauth.ErrNoClientID) {
		t.Errorf("RequestDeviceCode with no client: err = %v", err)
	}
}

// The flow stops at expires_in on its own, without waiting for GitHub to say
// so.
func TestDeviceFlowStopsAtExpiry(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	gh.Timing(1, 3)
	start := time.Now()
	if _, err := startFlow(t, gh.App()); !errors.Is(err, ghauth.ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if elapsed := time.Since(start); elapsed > 3*ghauthtest.Second+time.Second {
		t.Errorf("gave up after %v, well past expires_in", elapsed)
	}
	if polls := gh.Polls(); len(polls) > 3 {
		t.Errorf("polled %d times in 3 seconds at an interval of 1", len(polls))
	}
}

func TestDeviceFlowCancels(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	app := gh.App()
	dc, err := app.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := app.PollToken(ctx, dc)
		done <- err
	}()
	time.Sleep(3 * ghauthtest.Second)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PollToken did not return after its context was cancelled")
	}
}

// A poll that fails on the way is repeated; failing too often ends the flow.
func TestDeviceFlowRidesOutFailures(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	gh.Script("status:502", "status:502", "ok")
	if _, err := startFlow(t, gh.App()); err != nil {
		t.Errorf("after two failed polls: %v", err)
	}

	gh = ghauthtest.NewServer(t)
	gh.Script("status:502", "status:502", "status:502", "ok")
	var apiErr *ghauth.APIError
	if _, err := startFlow(t, gh.App()); !errors.As(err, &apiErr) || apiErr.StatusCode != 502 {
		t.Errorf("after three failed polls: err = %v", err)
	}

	// A refusal is not a failure to repeat.
	gh = ghauthtest.NewServer(t)
	gh.Script("status:400", "ok")
	if _, err := startFlow(t, gh.App()); !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Errorf("after a 400: err = %v", err)
	}
}

// A GitHub App's token expires and is refreshed with the client secret; the
// refresh token is spent doing so.
func TestRefresh(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	gh.Expiring(true)
	gh.Script("ok")
	app := gh.App()
	tok, err := startFlow(t, app)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if tok.RefreshToken == "" || !tok.ExpiresWithin(now, 9*time.Hour) || tok.ExpiresWithin(now, 7*time.Hour) || !tok.Refreshable(now) {
		t.Fatalf("a GitHub App token: expires %v, refresh token %v until %v", tok.ExpiresAt, tok.RefreshToken != "", tok.RefreshTokenExpiresAt)
	}

	fresh, err := app.Refresh(context.Background(), tok.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if fresh.AccessToken == tok.AccessToken || fresh.RefreshToken == tok.RefreshToken || fresh.ExpiresAt.IsZero() {
		t.Errorf("refreshed token = %+v", fresh)
	}
	if login, err := app.Login(context.Background(), fresh.AccessToken); err != nil || login != "octocat" {
		t.Errorf("the refreshed token's login = %q, %v", login, err)
	}

	var apiErr *ghauth.APIError
	if _, err := app.Refresh(context.Background(), tok.RefreshToken); !errors.As(err, &apiErr) || apiErr.Code != "bad_refresh_token" {
		t.Errorf("refreshing with a spent refresh token: err = %v", err)
	}
	app.ClientSecret = ""
	if _, err := app.Refresh(context.Background(), fresh.RefreshToken); !errors.Is(err, ghauth.ErrNoClientSecret) {
		t.Errorf("refreshing without the client secret: err = %v", err)
	}

	expired := ghauth.Token{RefreshToken: "r", RefreshTokenExpiresAt: now.Add(-time.Minute)}
	if expired.Refreshable(now) || (ghauth.Token{}).Refreshable(now) || (ghauth.Token{}).ExpiresWithin(now, time.Hour) {
		t.Error("a token without a live refresh token is refreshable, or one without an expiry expires")
	}
}

func TestRevokeAndLogin(t *testing.T) {
	gh := ghauthtest.NewServer(t)
	gh.AddToken("gho_alice", "", "alice")
	app := gh.App()
	ctx := context.Background()

	if err := app.Revoke(ctx, "gho_alice"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if got := gh.Revoked(); len(got) != 1 || got[0] != "gho_alice" {
		t.Errorf("revoked %v", got)
	}
	var apiErr *ghauth.APIError
	if _, err := app.Login(ctx, "gho_alice"); !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Errorf("Login with a revoked token: err = %v", err)
	}
	if err := app.Revoke(ctx, "gho_alice"); !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Errorf("revoking twice: err = %v", err)
	}
	app.ClientSecret = ""
	if err := app.Revoke(ctx, "gho_alice"); !errors.Is(err, ghauth.ErrNoClientSecret) {
		t.Errorf("revoking without the client secret: err = %v", err)
	}
}

// However a token or a credential ends up formatted or logged, the secret
// does not.
func TestTokensStayOutOfLogs(t *testing.T) {
	cred := ghauth.Credential{
		Token:       ghauth.Token{AccessToken: "gho_secret", RefreshToken: "ghr_secret"},
		GitHubLogin: "alice",
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("x", "cred", cred, "token", cred.Token, "ptr", &cred)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "cred", cred, "token", cred.Token)
	out := buf.String() + fmt.Sprintf("%v %+v %#v %s %v", cred, cred, cred, cred.Token, &cred.Token)
	if strings.Contains(out, "secret") {
		t.Errorf("a secret reached the output: %s", out)
	}
}
