package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// A client for the few calls of Pub/Sub's REST API a pull subscriber makes:
// pull, acknowledge, and modifyAckDeadline to hand a message back. The REST
// API needs nothing beyond golang.org/x/oauth2, where the official client
// library would bring in gRPC and a few dozen modules for the same three calls.

// pubsubScopes are the OAuth scopes the credentials are asked for, the same
// as Google's own Pub/Sub clients ask for. The pubsub scope is all pulling
// needs, but impersonated credentials need cloud-platform on their source to
// mint a token at all.
var pubsubScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/pubsub",
}

const (
	// pubsubEmulatorEnv names a Pub/Sub emulator's host:port. Every Google
	// client library honors it: plain HTTP, no credentials.
	pubsubEmulatorEnv = "PUBSUB_EMULATOR_HOST"
	// pubsubQuotaProjectEnv names the project to charge quota to, as it does
	// for Google's own client libraries; otherwise it is the credentials
	// file's quota_project_id, which `gcloud auth application-default
	// set-quota-project` writes.
	pubsubQuotaProjectEnv = "GOOGLE_CLOUD_QUOTA_PROJECT"
	// pubsubServerTimeout is how long Pub/Sub is asked to hold a pull open
	// while there is nothing to deliver (X-Server-Timeout). Left to itself it
	// holds a REST pull for about as long as a client would wait.
	pubsubServerTimeout = 60 * time.Second
	// pubsubTokenTimeout bounds fetching an access token, which a pull's own
	// deadline doesn't: the oauth2 transport fetches one before sending.
	pubsubTokenTimeout = 30 * time.Second
	// maxPubSubResponse bounds a pull's response. Pub/Sub sends at most 10 MB
	// of messages in one, a third more once base64-encoded.
	maxPubSubResponse = 64 << 20
)

// pubsubEndpoint is Pub/Sub's REST API. A variable so tests can stand in for
// it.
var pubsubEndpoint = "https://pubsub.googleapis.com/v1/"

// pubsubPullTimeout bounds one pull: well past pubsubServerTimeout, so a pull
// that runs out was not answered — the connection went quiet, as a laptop's
// does when it sleeps mid-pull — rather than waited on for good. A variable so
// tests can shorten it.
var pubsubPullTimeout = 2 * time.Minute

// pubsubCredentialTypes are the kinds of credentials file [Webhooks.PubSub]
// CredentialsFile may hold: what Application Default Credentials would accept
// from GOOGLE_APPLICATION_CREDENTIALS.
var pubsubCredentialTypes = map[google.CredentialsType]bool{
	google.ServiceAccount:                true,
	google.AuthorizedUser:                true,
	google.ExternalAccount:               true,
	google.ExternalAccountAuthorizedUser: true,
	google.ImpersonatedServiceAccount:    true,
}

// pubsubClient pulls from one subscription.
type pubsubClient struct {
	http *http.Client
	// base is the subscription's URL, which each call appends its method to.
	base string
	// quotaProject is sent as X-Goog-User-Project when set. User credentials
	// need one: without it Pub/Sub has no project of the caller's to charge.
	quotaProject string
}

// newPubSubClient builds a client for the subscription, authenticated with
// the credentials file when there is one and Application Default Credentials
// otherwise. With an emulator (PUBSUB_EMULATOR_HOST) it talks to that
// instead, without credentials.
func newPubSubClient(ctx context.Context, s pubsubSettings) (*pubsubClient, error) {
	path := subscriptionPath(s.subscription)
	if s.emulator != "" {
		return &pubsubClient{http: &http.Client{}, base: "http://" + s.emulator + "/v1/" + path}, nil
	}
	// The token source fetches with ctx, the subscriber's, so it outlives
	// each call's own; the HTTP client in it bounds each fetch.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: pubsubTokenTimeout})
	creds, err := pubsubCredentials(ctx, s.credentialsFile)
	if err != nil {
		return nil, err
	}
	source := markedTokenSource{oauth2.ReuseTokenSource(nil, creds.TokenSource)}
	client := &http.Client{Transport: &oauth2.Transport{Source: source}}
	return &pubsubClient{http: client, base: pubsubEndpoint + path, quotaProject: quotaProject(creds)}, nil
}

// markedTokenSource marks a failure to get an access token as a tokenError,
// however the credentials report it: a user login's or a key's refusal is an
// *oauth2.RetrieveError, an impersonated or workload identity one's only text.
type markedTokenSource struct{ oauth2.TokenSource }

func (s markedTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.TokenSource.Token()
	if err != nil {
		return nil, &tokenError{err}
	}
	return token, nil
}

// tokenError is a failure to get an access token.
type tokenError struct{ err error }

func (e *tokenError) Error() string { return e.err.Error() }
func (e *tokenError) Unwrap() error { return e.err }

// subscriptionPath is a subscription's name as a URL path. Pub/Sub allows a
// "%" in a name, which has to be escaped there.
func subscriptionPath(subscription string) string {
	segments := strings.Split(subscription, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// quotaProject is the project Pub/Sub should charge the calls to, when the
// environment or the credentials name one.
func quotaProject(creds *google.Credentials) string {
	if p := strings.TrimSpace(os.Getenv(pubsubQuotaProjectEnv)); p != "" {
		return p
	}
	var file struct {
		QuotaProjectID string `json:"quota_project_id"`
	}
	if len(creds.JSON) > 0 && json.Unmarshal(creds.JSON, &file) == nil {
		return strings.TrimSpace(file.QuotaProjectID)
	}
	return ""
}

// pubsubCredentials reads the credentials to pull with: the file when one is
// named, Application Default Credentials otherwise.
func pubsubCredentials(ctx context.Context, credentialsFile string) (*google.Credentials, error) {
	if credentialsFile == "" {
		creds, err := google.FindDefaultCredentials(ctx, pubsubScopes...)
		if err != nil {
			return nil, fmt.Errorf("no Application Default Credentials (run `gcloud auth application-default login`, or set [Webhooks.PubSub] CredentialsFile): %w", err)
		}
		return creds, nil
	}
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("reading [Webhooks.PubSub] CredentialsFile: %w", err)
	}
	var file struct {
		Type google.CredentialsType `json:"type"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("[Webhooks.PubSub] CredentialsFile %s is not a credentials JSON file: %w", credentialsFile, err)
	}
	if !pubsubCredentialTypes[file.Type] {
		return nil, fmt.Errorf("[Webhooks.PubSub] CredentialsFile %s holds %q credentials, not a service account key or Application Default Credentials", credentialsFile, file.Type)
	}
	return google.CredentialsFromJSONWithType(ctx, data, file.Type, pubsubScopes...)
}

// pubsubMessage is one message a pull returned.
type pubsubMessage struct {
	AckID   string `json:"ackId"`
	Message struct {
		// Data is the message's payload; encoding/json decodes the API's
		// base64 into it.
		Data        []byte            `json:"data"`
		Attributes  map[string]string `json:"attributes"`
		MessageID   string            `json:"messageId"`
		PublishTime time.Time         `json:"publishTime"`
	} `json:"message"`
	DeliveryAttempt int `json:"deliveryAttempt"`
}

// pubsubError is an error the API answered with.
type pubsubError struct {
	HTTPStatus int
	Status     string // the gRPC status name, "NOT_FOUND"
	Message    string
}

func (e *pubsubError) Error() string {
	if e.Status == "" {
		return fmt.Sprintf("pubsub: HTTP %d: %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("pubsub: %s: %s", e.Status, e.Message)
}

// permanent reports whether retrying can't help until something outside the
// server changes: the subscription is missing, or the credentials may not
// pull from it.
func (e *pubsubError) permanent() bool {
	switch e.HTTPStatus {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest:
		return true
	}
	return false
}

// permanentPubSubError reports whether retrying err can't help until
// something outside the server changes: Pub/Sub refused the subscription or
// the credentials, or Google refused to issue a token for them — an expired
// login, a revoked or deleted key.
func permanentPubSubError(err error) bool {
	var apiErr *pubsubError
	if errors.As(err, &apiErr) {
		return apiErr.permanent()
	}
	var refused *oauth2.RetrieveError
	if errors.As(err, &refused) && refused.Response != nil {
		switch status := refused.Response.StatusCode; status {
		case http.StatusRequestTimeout, http.StatusTooManyRequests:
			return false
		default:
			return status >= 400 && status < 500
		}
	}
	return false
}

// credentialsError reports whether err came from getting an access token,
// which reading the credentials again might fix.
func credentialsError(err error) bool {
	var tokenErr *tokenError
	return errors.As(err, &tokenErr)
}

// pull waits for messages on the subscription and returns up to max of them.
// Pub/Sub holds the request open until there are some or pubsubServerTimeout
// passes, when it answers with none — or with DEADLINE_EXCEEDED, which is
// taken as none too. A pull unanswered after pubsubPullTimeout fails.
func (c *pubsubClient) pull(ctx context.Context, max int) ([]pubsubMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, pubsubPullTimeout)
	defer cancel()
	var resp struct {
		ReceivedMessages []pubsubMessage `json:"receivedMessages"`
	}
	header := http.Header{"X-Server-Timeout": {fmt.Sprint(int(pubsubServerTimeout.Seconds()))}}
	err := c.call(ctx, "pull", map[string]any{"maxMessages": max}, &resp, header)
	if apiErr := (*pubsubError)(nil); errors.As(err, &apiErr) && apiErr.HTTPStatus == http.StatusGatewayTimeout {
		return nil, nil
	}
	return resp.ReceivedMessages, err
}

// acknowledge tells Pub/Sub the messages were handled, so it doesn't deliver
// them again.
func (c *pubsubClient) acknowledge(ctx context.Context, ackIDs []string) error {
	if len(ackIDs) == 0 {
		return nil
	}
	return c.call(ctx, "acknowledge", map[string]any{"ackIds": ackIDs}, nil, nil)
}

// release hands the messages back for Pub/Sub to deliver again, now rather
// than once their ack deadline passes.
func (c *pubsubClient) release(ctx context.Context, ackIDs []string) error {
	if len(ackIDs) == 0 {
		return nil
	}
	return c.call(ctx, "modifyAckDeadline", map[string]any{"ackIds": ackIDs, "ackDeadlineSeconds": 0}, nil, nil)
}

// call posts body to the subscription's method, with header besides the usual
// ones, and decodes the answer into out, when it wants one.
func (c *pubsubClient) call(ctx context.Context, method string, body, out any, header http.Header) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+":"+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for key, values := range header {
		req.Header[key] = values
	}
	req.Header.Set("Content-Type", "application/json")
	if c.quotaProject != "" {
		req.Header.Set("X-Goog-User-Project", c.quotaProject)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPubSubResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &pubsubError{HTTPStatus: resp.StatusCode}
		var envelope struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Message != "" {
			apiErr.Status, apiErr.Message = envelope.Error.Status, envelope.Error.Message
		} else {
			apiErr.Message = strings.TrimSpace(string(data))
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
