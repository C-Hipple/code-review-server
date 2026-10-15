package git_tools

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// etagServer serves its current body with an ETag derived from it and, like
// GitHub, answers a request repeating that ETag with a bare 304 carrying a
// fresh rate limit reading. It records the If-None-Match of every request.
type etagServer struct {
	*httptest.Server
	mu          sync.Mutex
	body        string
	noValidator bool
	link        string
	asked       []string
}

func newETagServer(t *testing.T, body string) *etagServer {
	t.Helper()
	s := &etagServer{body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		body, noValidator, link := s.body, s.noValidator, s.link
		s.asked = append(s.asked, r.Header.Get("If-None-Match"))
		remaining := 5000 - len(s.asked)
		s.mu.Unlock()

		w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(remaining))
		etag := fmt.Sprintf(`W/"%x"`, sha256.Sum256([]byte(r.Header.Get("Accept")+body)))
		if !noValidator && r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if !noValidator {
			w.Header().Set("ETag", etag)
		}
		if link != "" {
			w.Header().Set("Link", link)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

// update changes how the server answers from the next request on.
func (s *etagServer) update(change func(s *etagServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

// validators returns the If-None-Match each request so far carried.
func (s *etagServer) validators() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func cachingClient(cache *responseCache) *http.Client {
	return &http.Client{Transport: &revalidatingRoundTripper{next: http.DefaultTransport, cache: cache}}
}

// get fetches url and returns the response with its body read.
func get(t *testing.T, client *http.Client, url string, header ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, string(body)
}

func TestResponseCacheAnswersA304WithTheStoredReply(t *testing.T) {
	srv := newETagServer(t, `{"v":1}`)
	cache := newResponseCache(1<<20, 1<<16)
	client := cachingClient(cache)

	get(t, client, srv.URL+"/pulls/1")
	resp, body := get(t, client, srv.URL+"/pulls/1")

	asked := srv.validators()
	if asked[0] != "" || asked[1] == "" {
		t.Fatalf("validators sent = %q, want none on the first request and the ETag on the second", asked)
	}
	if resp.StatusCode != http.StatusOK || body != `{"v":1}` {
		t.Errorf("revalidated reply = %d %q, want 200 with the stored body", resp.StatusCode, body)
	}
	if resp.Header.Get("X-From-Cache") != "1" {
		t.Error("revalidated reply not marked X-From-Cache")
	}
	// The stored reply's description of its body stands; the 304's own
	// headers — the rate limit reading above all — replace the rest.
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want the stored reply's", got)
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "4998" {
		t.Errorf("X-RateLimit-Remaining = %q, want the 304's 4998", got)
	}
	if stats := cache.stats(); stats.NotModified != 1 || stats.Entries != 1 {
		t.Errorf("stats = %+v, want one 304 and one entry", stats)
	}
}

func TestResponseCacheReplacesAReplyThatChanged(t *testing.T) {
	srv := newETagServer(t, `{"v":1}`)
	client := cachingClient(newResponseCache(1<<20, 1<<16))

	get(t, client, srv.URL+"/pulls/1")
	srv.update(func(s *etagServer) { s.body = `{"v":2}` })
	if _, body := get(t, client, srv.URL+"/pulls/1"); body != `{"v":2}` {
		t.Fatalf("body = %q after the resource changed, want the new one", body)
	}
	// The new version is what is revalidated from now on.
	if resp, body := get(t, client, srv.URL+"/pulls/1"); body != `{"v":2}` || resp.Header.Get("X-From-Cache") != "1" {
		t.Errorf("third reply = %q (from cache: %q), want the new body from a 304", body, resp.Header.Get("X-From-Cache"))
	}
}

// One URL serves several representations — a PR as JSON or as a diff — so a
// stored JSON reply must never stand in for the diff.
func TestResponseCacheKeysByAccept(t *testing.T) {
	srv := newETagServer(t, `body`)
	client := cachingClient(newResponseCache(1<<20, 1<<16))

	get(t, client, srv.URL+"/pulls/1", "Accept", "application/json")
	get(t, client, srv.URL+"/pulls/1", "Accept", "application/vnd.github.v3.diff")

	if asked := srv.validators(); asked[1] != "" {
		t.Errorf("the diff request carried the JSON reply's ETag %q", asked[1])
	}
}

func TestResponseCacheLeavesOtherMethodsAlone(t *testing.T) {
	srv := newETagServer(t, `{}`)
	cache := newResponseCache(1<<20, 1<<16)
	client := cachingClient(cache)

	for range 2 {
		resp, err := client.Post(srv.URL+"/graphql", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
	}
	if asked := srv.validators(); asked[1] != "" {
		t.Errorf("a POST carried If-None-Match %q", asked[1])
	}
	if stats := cache.stats(); stats.Entries != 0 {
		t.Errorf("stats = %+v, want nothing stored for a POST", stats)
	}
}

func TestResponseCacheSkipsRepliesWithoutValidators(t *testing.T) {
	srv := newETagServer(t, `{}`)
	srv.update(func(s *etagServer) { s.noValidator = true })
	cache := newResponseCache(1<<20, 1<<16)
	client := cachingClient(cache)

	get(t, client, srv.URL+"/rate_limit")
	get(t, client, srv.URL+"/rate_limit")

	if stats := cache.stats(); stats.Entries != 0 {
		t.Errorf("stats = %+v, want nothing stored without an ETag or Last-Modified", stats)
	}
}

// A caller that sends its own validators gets GitHub's answer as it is,
// 304 included.
func TestResponseCacheLeavesACallersOwnValidatorsAlone(t *testing.T) {
	srv := newETagServer(t, `{}`)
	client := cachingClient(newResponseCache(1<<20, 1<<16))

	first, _ := get(t, client, srv.URL+"/pulls/1")
	resp, _ := get(t, client, srv.URL+"/pulls/1", "If-None-Match", first.Header.Get("ETag"))
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want the 304 itself", resp.StatusCode)
	}
}

func TestResponseCachePassesATooBigReplyThroughWhole(t *testing.T) {
	big := strings.Repeat("x", 100)
	srv := newETagServer(t, big)
	cache := newResponseCache(1<<20, 10)
	client := cachingClient(cache)

	if _, body := get(t, client, srv.URL+"/pulls/1.diff"); body != big {
		t.Errorf("got %d bytes, want all %d", len(body), len(big))
	}
	get(t, client, srv.URL+"/pulls/1.diff")
	if asked := srv.validators(); asked[1] != "" {
		t.Errorf("an uncached reply was revalidated with %q", asked[1])
	}
	if stats := cache.stats(); stats.Entries != 0 {
		t.Errorf("stats = %+v, want nothing stored", stats)
	}
}

// go-github reads the next page off the Link header, and a 304 doesn't repeat
// it: without the stored one, revalidating page one would end the listing.
func TestResponseCacheKeepsTheStoredLinkHeader(t *testing.T) {
	srv := newETagServer(t, `[1]`)
	const link = `<https://api.github.com/x?page=2>; rel="next"`
	srv.update(func(s *etagServer) { s.link = link })
	client := cachingClient(newResponseCache(1<<20, 1<<16))

	get(t, client, srv.URL+"/x")
	resp, _ := get(t, client, srv.URL+"/x")
	if resp.Header.Get("X-From-Cache") != "1" || resp.Header.Get("Link") != link {
		t.Errorf("revalidated Link = %q, want the stored %q", resp.Header.Get("Link"), link)
	}
}

func TestResponseCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	entry := func(key string) *cachedResponse {
		return &cachedResponse{key: key, etag: `"` + key + `"`, body: []byte("body")}
	}
	// Room for exactly three entries.
	cache := newResponseCache(3*entry("a").size(), 1<<10)
	cache.put(entry("a"))
	cache.put(entry("b"))
	cache.put(entry("c"))
	cache.get("a") // a is now more recently used than b
	cache.put(entry("d"))

	if cache.get("b") != nil {
		t.Error("b survived, want it evicted as the least recently used")
	}
	for _, key := range []string{"a", "c", "d"} {
		if cache.get(key) == nil {
			t.Errorf("%s was evicted", key)
		}
	}
	if stats := cache.stats(); stats.Entries != 3 || stats.Bytes != 3*entry("a").size() {
		t.Errorf("stats = %+v, want 3 entries filling the cache exactly", stats)
	}
}

func TestResponseCacheIsSafeForConcurrentUse(t *testing.T) {
	srv := newETagServer(t, `{"v":1}`)
	client := cachingClient(newResponseCache(1<<20, 1<<16))

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body := get(t, client, fmt.Sprintf("%s/pulls/%d", srv.URL, i%4))
			if body != `{"v":1}` {
				t.Errorf("body = %q", body)
			}
		}()
	}
	wg.Wait()
}
