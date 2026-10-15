package git_tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

func rateHeaders(resource string, remaining, limit int) http.Header {
	h := http.Header{}
	if resource != "" {
		h.Set("X-RateLimit-Resource", resource)
	}
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
	return h
}

// A search reply reports search's 30-a-minute budget under the same header
// names as REST's. Read as the REST budget, a couple of searches would put
// every REST request into the throttle.
func TestSearchRepliesDoNotTouchTheRESTBudget(t *testing.T) {
	m := NewRateLimitManager()
	m.UpdateFromHeaders(RateResourceCore, rateHeaders("core", 4000, 5000))
	// The transport expected core, but the reply says what it was charged to.
	m.UpdateFromHeaders(RateResourceCore, rateHeaders("search", 3, 30))

	if core := m.GetStatus(); core.Remaining != 4000 || core.Limit != 5000 {
		t.Errorf("core = %d/%d, want the 4000/5000 core reply", core.Remaining, core.Limit)
	}
	if search := m.GetStatusFor(RateResourceSearch); search.Remaining != 3 || search.Limit != 30 {
		t.Errorf("search = %d/%d, want 3/30", search.Remaining, search.Limit)
	}
}

// A reply that doesn't name its budget is charged to the one its transport
// expected.
func TestRepliesWithoutAResourceUseTheFallback(t *testing.T) {
	m := NewRateLimitManager()
	m.UpdateFromHeaders(RateResourceGraphQL, rateHeaders("", 4321, 5000))

	if graphQL := m.GetStatusFor(RateResourceGraphQL); graphQL.Remaining != 4321 {
		t.Errorf("graphql remaining = %d, want 4321", graphQL.Remaining)
	}
	if core := m.GetStatus(); core.Remaining != defaultRateLimit {
		t.Errorf("core remaining = %d, want it untouched", core.Remaining)
	}
}

func TestRESTRequestsAreChargedToTheirBudget(t *testing.T) {
	for path, want := range map[string]string{
		"/search/issues":            RateResourceSearch,
		"/repos/acme/widgets/pulls": RateResourceCore,
		"/user/teams":               RateResourceCore,
	} {
		req := &http.Request{URL: &url.URL{Path: path}}
		if got := restRateResource(req); got != want {
			t.Errorf("restRateResource(%s) = %q, want %q", path, got, want)
		}
	}
}

// A spent budget holds back only the requests that would spend it.
func TestWaitIfNeededWaitsOnlyOnTheSpentBudget(t *testing.T) {
	m := NewRateLimitManager()
	m.UpdateFromHeaders(RateResourceSearch, rateHeaders("search", 0, 30))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.WaitIfNeeded(ctx, RateResourceSearch); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("search wait = %v, want it held until the deadline", err)
	}
	if err := m.WaitIfNeeded(context.Background(), RateResourceCore); err != nil {
		t.Errorf("core wait = %v, want no wait at all", err)
	}
}

func TestThrottleThresholdsScaleWithTheBudget(t *testing.T) {
	m := NewRateLimitManager()
	for _, tc := range []struct{ limit, throttleAt, reserve int }{
		{5000, 100, 10},
		{30, 0, 0},
		{0, 100, 10}, // a reply that didn't say: assume the usual budget
	} {
		throttleAt, reserve := m.thresholds(tc.limit)
		if throttleAt != tc.throttleAt || reserve != tc.reserve {
			t.Errorf("thresholds(%d) = (%d, %d), want (%d, %d)", tc.limit, throttleAt, reserve, tc.throttleAt, tc.reserve)
		}
	}
}

// /rate_limit reports every budget at once, which is how the GraphQL budget is
// known even when nothing has spent from it lately.
func TestGetRateLimitFromAPIRecordsEveryBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"resources":{
			"core":{"limit":5000,"remaining":4900,"reset":1900000000},
			"graphql":{"limit":5000,"remaining":1234,"reset":1900000000},
			"search":{"limit":30,"remaining":29,"reset":1900000000}}}`)
	}))
	t.Cleanup(srv.Close)
	client := github.NewClient(nil)
	base, _ := url.Parse(srv.URL + "/")
	client.BaseURL = base

	limit, remaining, _, err := GetRateLimitFromAPI(client)
	if err != nil {
		t.Fatalf("GetRateLimitFromAPI: %v", err)
	}
	if limit != 5000 || remaining != 4900 {
		t.Errorf("core = %d/%d, want 4900/5000", remaining, limit)
	}
	if graphQL := GetRateLimitStatusFor(RateResourceGraphQL); graphQL.Remaining != 1234 {
		t.Errorf("graphql remaining = %d, want 1234", graphQL.Remaining)
	}
	if search := GetRateLimitStatusFor(RateResourceSearch); search.Remaining != 29 || search.Limit != 30 {
		t.Errorf("search = %d/%d, want 29/30", search.Remaining, search.Limit)
	}
}
