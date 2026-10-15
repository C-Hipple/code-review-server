// Package ghauth links a hosted server's users to their own GitHub accounts,
// so a review or a merge they make through the server is recorded by GitHub
// as theirs rather than as the server token's.
//
// It speaks GitHub's OAuth device flow: the server shows the user a short
// code, the user enters it at github.com, and the server polls until GitHub
// hands over a token. Nothing redirects back to the server, so a server on an
// internal network can link accounts as well as one on the internet. The
// package also refreshes the expiring tokens a GitHub App issues, revokes a
// token, asks GitHub whose a token is, and seals a token for storage
// (seal.go).
//
// It imports nothing from the module: the server decides when to link a user
// and where the sealed credential lives.
package ghauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultScopes is what an OAuth App asks for: repo covers reviewing,
	// replying to and merging pull requests in private repositories as well as
	// public ones. A GitHub App ignores scopes and grants the permissions it
	// was configured with.
	DefaultScopes = "repo"
	// DefaultTimeout bounds one HTTP call to GitHub.
	DefaultTimeout = 30 * time.Second

	// defaultInterval and defaultExpiresIn stand in for a device code
	// response that leaves them out; they are GitHub's own defaults.
	defaultInterval  = 5
	defaultExpiresIn = 900
	// slowDownSeconds is what a slow_down answer adds to the polling interval.
	slowDownSeconds = 5
	// maxPollFailures is how many polls in a row may fail on the way to
	// GitHub — a dropped connection, a 502 — before the flow gives up. One
	// failed poll is no reason to make the user start over.
	maxPollFailures = 3
	// maxResponseBytes caps what is read of a response; GitHub's are a few
	// hundred bytes.
	maxResponseBytes = 1 << 20
)

// Endpoints are where an App reaches GitHub. Only tests change them.
type Endpoints struct {
	// DeviceCode starts a device flow.
	DeviceCode string
	// AccessToken exchanges a device code, or a refresh token, for a token.
	AccessToken string
	// API is the REST API's root, for GET /user and revoking a token.
	API string
}

// GitHub is github.com's endpoints.
var GitHub = Endpoints{
	DeviceCode:  "https://github.com/login/device/code",
	AccessToken: "https://github.com/login/oauth/access_token",
	API:         "https://api.github.com",
}

var (
	// ErrNoClientID means the App has no client ID to start a flow with.
	ErrNoClientID = errors.New("no GitHub OAuth client ID configured")
	// ErrNoClientSecret means a call that needs the client secret — refreshing
	// or revoking a token — was made without one.
	ErrNoClientSecret = errors.New("no GitHub OAuth client secret configured")
	// ErrExpired means the device code expired before the user authorized it.
	ErrExpired = errors.New("the device code expired before it was authorized")
	// ErrDenied means the user declined to authorize the app.
	ErrDenied = errors.New("the authorization was declined")
	// ErrMalformed wraps a response that is not what GitHub sends.
	ErrMalformed = errors.New("malformed response from GitHub")
)

// APIError is an error GitHub answered with: a non-2xx status, or the error
// field of a 200, which is how the OAuth endpoints report most of theirs.
type APIError struct {
	StatusCode int
	// Code is the OAuth error code, e.g. "device_flow_disabled" or
	// "bad_refresh_token"; empty for an error of the REST API.
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("GitHub answered %s: %s", e.Code, e.Message)
	case e.Code != "":
		return "GitHub answered " + e.Code
	default:
		return fmt.Sprintf("GitHub answered status %d: %s", e.StatusCode, e.Message)
	}
}

// App is the OAuth App or GitHub App a server links accounts through. Device
// flow has to be enabled in its settings on GitHub.
type App struct {
	ClientID string
	// ClientSecret is optional. The device flow needs none, but refreshing an
	// expiring token and revoking one do.
	ClientSecret string
	// Scopes is the OAuth scopes to ask for, separated by spaces or commas;
	// empty asks for none.
	Scopes string

	// Endpoints is GitHub's when zero, and HTTP a client with DefaultTimeout
	// when nil. Second is how long one of the device flow's seconds lasts —
	// its interval and expires_in are counted in them — and is time.Second
	// when zero. Tests change all three.
	Endpoints Endpoints
	HTTP      *http.Client
	Second    time.Duration
}

func (a *App) endpoints() Endpoints {
	if a.Endpoints == (Endpoints{}) {
		return GitHub
	}
	return a.Endpoints
}

func (a *App) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: DefaultTimeout}
}

func (a *App) second() time.Duration {
	if a.Second > 0 {
		return a.Second
	}
	return time.Second
}

// Token is a GitHub user access token, as the token endpoint grants it. An
// OAuth App's never expires and has no refresh token; a GitHub App's expires
// (after eight hours, by default) and comes with a refresh token good for
// months.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresAt and RefreshTokenExpiresAt are zero for a token that does not
	// expire.
	ExpiresAt             time.Time `json:"expires_at"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

// ExpiresWithin reports whether the token expires before now+d.
func (t Token) ExpiresWithin(now time.Time, d time.Duration) bool {
	return !t.ExpiresAt.IsZero() && t.ExpiresAt.Before(now.Add(d))
}

// Refreshable reports whether the token has a refresh token still good at
// now.
func (t Token) Refreshable(now time.Time) bool {
	return t.RefreshToken != "" && (t.RefreshTokenExpiresAt.IsZero() || now.Before(t.RefreshTokenExpiresAt))
}

// String, GoString and LogValue keep a token out of logs and error messages,
// however it ends up formatted.
func (t Token) String() string       { return "ghauth.Token(redacted)" }
func (t Token) GoString() string     { return t.String() }
func (t Token) LogValue() slog.Value { return slog.StringValue(t.String()) }

// tokenResponse is the token endpoint's answer, a token or an OAuth error.
type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
	// Interval is the new polling interval a slow_down answer carries.
	Interval int `json:"interval"`
}

// token is the granted token, its lifetimes counted from now.
func (r tokenResponse) token(now time.Time) *Token {
	t := &Token{AccessToken: r.AccessToken, TokenType: r.TokenType, Scope: r.Scope, RefreshToken: r.RefreshToken}
	if r.ExpiresIn > 0 {
		t.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if r.RefreshTokenExpiresIn > 0 {
		t.RefreshTokenExpiresAt = now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)
	}
	return t
}

// DeviceCode is a started device flow. DeviceCode is what the server polls
// with, and as good as the token to whoever holds it and the client ID, so it
// stays in memory and out of logs; UserCode and VerificationURI are what the
// user is shown.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	// Interval is the seconds GitHub asks to be left between polls.
	Interval int
	// ExpiresAt is when the code stops working: expires_in after GitHub
	// issued it.
	ExpiresAt time.Time
}

// RequestDeviceCode starts a device flow.
func (a *App) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	if a.ClientID == "" {
		return nil, ErrNoClientID
	}
	form := url.Values{"client_id": {a.ClientID}}
	if scopes := normalizeScopes(a.Scopes); scopes != "" {
		form.Set("scope", scopes)
	}
	var resp struct {
		DeviceCode       string `json:"device_code"`
		UserCode         string `json:"user_code"`
		VerificationURI  string `json:"verification_uri"`
		ExpiresIn        int    `json:"expires_in"`
		Interval         int    `json:"interval"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	issued := time.Now()
	if err := a.postForm(ctx, a.endpoints().DeviceCode, form, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, &APIError{StatusCode: http.StatusOK, Code: resp.Error, Message: resp.ErrorDescription}
	}
	if resp.DeviceCode == "" || resp.UserCode == "" || resp.VerificationURI == "" {
		return nil, fmt.Errorf("%w: the device code response lacks a code or a URL", ErrMalformed)
	}
	interval, expiresIn := resp.Interval, resp.ExpiresIn
	if interval <= 0 {
		interval = defaultInterval
	}
	if expiresIn <= 0 {
		expiresIn = defaultExpiresIn
	}
	return &DeviceCode{
		DeviceCode:      resp.DeviceCode,
		UserCode:        resp.UserCode,
		VerificationURI: resp.VerificationURI,
		Interval:        interval,
		ExpiresAt:       issued.Add(time.Duration(expiresIn) * a.second()),
	}, nil
}

// PollToken polls GitHub for the token the user's authorization of dc grants,
// every Interval seconds — five more after each slow_down — until GitHub
// grants it or refuses, the code expires (ErrExpired), the user declines
// (ErrDenied), or ctx ends.
func (a *App) PollToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(dc.Interval) * a.second()
	failures := 0
	for {
		// Never past the code's expiry: a poll after it can only be refused.
		wait := interval
		remaining := time.Until(dc.ExpiresAt)
		if remaining <= 0 {
			return nil, ErrExpired
		}
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if !time.Now().Before(dc.ExpiresAt) {
			return nil, ErrExpired
		}

		form := url.Values{
			"client_id":   {a.ClientID},
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		var resp tokenResponse
		now := time.Now()
		if err := a.postForm(ctx, a.endpoints().AccessToken, form, &resp); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if failures++; failures >= maxPollFailures || !transient(err) {
				return nil, err
			}
			continue
		}
		failures = 0

		switch resp.Error {
		case "":
			if resp.AccessToken == "" {
				return nil, fmt.Errorf("%w: the token response has no token", ErrMalformed)
			}
			return resp.token(now), nil
		case "authorization_pending":
		case "slow_down":
			interval += slowDownSeconds * a.second()
			if given := time.Duration(resp.Interval) * a.second(); given > interval {
				interval = given
			}
		case "expired_token":
			return nil, ErrExpired
		case "access_denied":
			return nil, ErrDenied
		default:
			// incorrect_client_credentials, device_flow_disabled,
			// incorrect_device_code, unsupported_grant_type: none of which
			// another poll will change.
			return nil, &APIError{StatusCode: http.StatusOK, Code: resp.Error, Message: resp.ErrorDescription}
		}
	}
}

// transient reports whether a failed poll is worth repeating: a failure on
// the way to GitHub, or GitHub failing on its side, rather than GitHub
// refusing the request.
func transient(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500 || apiErr.StatusCode == http.StatusTooManyRequests
	}
	return !errors.Is(err, ErrMalformed)
}

// Refresh exchanges a refresh token for a new token. GitHub rotates the
// refresh token as it does, so the old one is spent whatever happens to the
// new one: store it before using it. A refresh token GitHub no longer accepts
// is an *APIError with Code "bad_refresh_token".
func (a *App) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	if a.ClientSecret == "" {
		return nil, ErrNoClientSecret
	}
	form := url.Values{
		"client_id":     {a.ClientID},
		"client_secret": {a.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	var resp tokenResponse
	now := time.Now()
	if err := a.postForm(ctx, a.endpoints().AccessToken, form, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, &APIError{StatusCode: http.StatusOK, Code: resp.Error, Message: resp.ErrorDescription}
	}
	if resp.AccessToken == "" {
		return nil, fmt.Errorf("%w: the refresh response has no token", ErrMalformed)
	}
	return resp.token(now), nil
}

// Revoke revokes an access token, which GitHub allows only the app it was
// issued to, authenticated by its client secret.
func (a *App) Revoke(ctx context.Context, accessToken string) error {
	if a.ClientSecret == "" {
		return ErrNoClientSecret
	}
	body, err := json.Marshal(map[string]string{"access_token": accessToken})
	if err != nil {
		return err
	}
	endpoint := a.endpoints().API + "/applications/" + url.PathEscape(a.ClientID) + "/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(a.ClientID, a.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	_, err = a.do(req)
	return err
}

// Login returns the login of the GitHub account accessToken belongs to. A
// token GitHub does not accept is an *APIError with StatusCode 401.
func (a *App) Login(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoints().API+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	body, err := a.do(req)
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil || user.Login == "" {
		return "", fmt.Errorf("%w: GET /user has no login", ErrMalformed)
	}
	return user.Login, nil
}

// postForm posts a form to one of GitHub's OAuth endpoints and decodes the
// JSON it answers into out.
func (a *App) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The OAuth endpoints answer form-encoded unless asked for JSON.
	req.Header.Set("Accept", "application/json")
	body, err := a.do(req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return nil
}

// do sends req and returns the body of a 2xx answer, or the error GitHub
// answered with. Neither the request nor the response is ever logged: both
// can carry tokens.
func (a *App) do(req *http.Request) ([]byte, error) {
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return body, nil
	}
	apiErr := &APIError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	var detail struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
	}
	if json.Unmarshal(body, &detail) == nil {
		apiErr.Code = detail.Error
		switch {
		case detail.ErrorDescription != "":
			apiErr.Message = detail.ErrorDescription
		case detail.Message != "":
			apiErr.Message = detail.Message
		}
	}
	return nil, apiErr
}

// normalizeScopes joins scopes separated by spaces or commas with single
// spaces, as the device code endpoint expects them.
func normalizeScopes(scopes string) string {
	return strings.Join(strings.FieldsFunc(scopes, func(r rune) bool { return r == ' ' || r == ',' }), " ")
}
