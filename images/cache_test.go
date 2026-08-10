package images

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// newTestStore returns a store pointed at a temp directory that will fetch from
// the local test servers below, which the real GitHub allowlist rejects.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	s.allowHost = func(string) bool { return true }
	s.token = func() string { return "" }
	return s
}

func servePNG(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "image/png")
	fmt.Fprint(w, body)
}

func TestResolveDownloadsThenServesFromDisk(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		servePNG(w, "png-bytes")
	}))
	defer server.Close()

	store := newTestStore(t)
	entry, cached, err := store.Resolve(server.URL + "/assets/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cached {
		t.Error("first Resolve() reported a cache hit")
	}
	if entry.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", entry.ContentType)
	}
	if entry.Size != int64(len("png-bytes")) {
		t.Errorf("Size = %d, want %d", entry.Size, len("png-bytes"))
	}
	if !strings.HasSuffix(entry.Path, ".png") {
		t.Errorf("Path = %q, want a .png extension so clients can type it", entry.Path)
	}
	body, err := os.ReadFile(entry.Path)
	if err != nil {
		t.Fatalf("reading the cached file: %v", err)
	}
	if string(body) != "png-bytes" {
		t.Errorf("cached bytes = %q, want %q", body, "png-bytes")
	}

	again, cached, err := store.Resolve(server.URL + "/assets/abc")
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if !cached {
		t.Error("second Resolve() went back to the network")
	}
	if again.Path != entry.Path {
		t.Errorf("second Resolve() path = %q, want %q", again.Path, entry.Path)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1", got)
	}
}

func TestResolveSendsTheTokenAndDropsItOnTheRedirect(t *testing.T) {
	// GitHub rejects a request that carries both the signature in its redirect
	// target's query string and an Authorization header, so the token has to
	// come off after the first hop.
	var authHeaders []string
	var mu sync.Mutex
	record := func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
	}

	signed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r)
		servePNG(w, "png-bytes")
	}))
	defer signed.Close()

	attachment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r)
		http.Redirect(w, r, signed.URL+"/signed?jwt=abc", http.StatusFound)
	}))
	defer attachment.Close()

	store := newTestStore(t)
	store.token = func() string { return "ghp_token" }
	if _, _, err := store.Resolve(attachment.URL + "/assets/abc"); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bearer ghp_token", ""}
	if len(authHeaders) != len(want) {
		t.Fatalf("got %d requests (%v), want %d", len(authHeaders), authHeaders, len(want))
	}
	for i, w := range want {
		if authHeaders[i] != w {
			t.Errorf("request %d Authorization = %q, want %q", i, authHeaders[i], w)
		}
	}
}

func TestResolveRefusesARedirectOffTheAllowlist(t *testing.T) {
	attachment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.test/pixel.png", http.StatusFound)
	}))
	defer attachment.Close()

	store := newTestStore(t)
	// Everything but the redirect target is allowed, so the refusal is the
	// hop check rather than the initial validation.
	store.allowHost = func(host string) bool { return host != "evil.test" }

	_, _, err := store.Resolve(attachment.URL + "/assets/abc")
	if err == nil {
		t.Fatal("Resolve() followed a redirect off the allowlist")
	}
	if !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Errorf("Resolve() error = %v, want a refused-redirect error", err)
	}
}

func TestResolveRejectsNonImageResponses(t *testing.T) {
	// A sign-in page comes back as a 200 with HTML; caching it would hand every
	// client something it can't render.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>sign in</html>")
	}))
	defer server.Close()

	store := newTestStore(t)
	if _, _, err := store.Resolve(server.URL + "/assets/abc"); err == nil {
		t.Fatal("Resolve() accepted an HTML response")
	}
	if _, ok := store.Lookup(server.URL + "/assets/abc"); ok {
		t.Error("a non-image response was written to the cache")
	}
}

func TestResolveRejectsAnErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	store := newTestStore(t)
	_, _, err := store.Resolve(server.URL + "/assets/gone")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("Resolve() error = %v, want a 404 error", err)
	}
}

func TestResolveRemembersAFailureBriefly(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	store := newTestStore(t)
	for i := 0; i < 3; i++ {
		if _, _, err := store.Resolve(server.URL + "/assets/gone"); err == nil {
			t.Fatal("Resolve() succeeded against a 404")
		}
	}
	// A broken URL in an old comment shouldn't be retried every time the PR is
	// looked at.
	if got := requests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1", got)
	}
}

func TestResolveRejectsAnOversizedImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(make([]byte, MaxImageBytes+1))
	}))
	defer server.Close()

	store := newTestStore(t)
	_, _, err := store.Resolve(server.URL + "/assets/huge")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Resolve() error = %v, want a size-limit error", err)
	}
	if _, ok := store.Lookup(server.URL + "/assets/huge"); ok {
		t.Error("an oversized image was written to the cache")
	}
}

func TestConcurrentResolvesDownloadOnce(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		<-release
		servePNG(w, "png-bytes")
	}))
	defer server.Close()

	store := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := store.Resolve(server.URL + "/assets/abc"); err != nil {
				t.Errorf("Resolve() error = %v", err)
			}
		}()
	}
	close(release)
	wg.Wait()

	if got := requests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1 — concurrent resolves should collapse", got)
	}
}

func TestPrefetchFillsTheCacheAndSkipsWhatIsThere(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		servePNG(w, "png-bytes")
	}))
	defer server.Close()

	store := newTestStore(t)
	urls := []string{
		server.URL + "/assets/one",
		server.URL + "/assets/two",
		// Not fetchable, and must not stop the others.
		"https://evil.test/pixel.png",
	}
	store.allowHost = func(host string) bool { return host != "evil.test" }

	store.Prefetch(urls)
	if got := requests.Load(); got != 2 {
		t.Errorf("made %d requests, want 2", got)
	}
	for _, u := range urls[:2] {
		if _, ok := store.Lookup(u); !ok {
			t.Errorf("Lookup(%q) = false after Prefetch", u)
		}
	}

	store.Prefetch(urls)
	if got := requests.Load(); got != 2 {
		t.Errorf("made %d requests after a second Prefetch, want 2 — cached images should be skipped", got)
	}
}

func TestLookupNeverTouchesTheNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		servePNG(w, "png-bytes")
	}))
	defer server.Close()

	store := newTestStore(t)
	if _, ok := store.Lookup(server.URL + "/assets/abc"); ok {
		t.Error("Lookup() reported a hit for an image that was never fetched")
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("Lookup() made %d requests, want 0", got)
	}
}

func TestResolveValidatesTheURL(t *testing.T) {
	// The real allowlist, i.e. what the RPC handler enforces.
	store := NewStore(t.TempDir())
	for _, raw := range []string{
		"https://evil.test/pixel.png",
		"http://192.168.1.1/admin",
		"file:///etc/passwd",
		"not a url at all",
		"",
	} {
		if _, _, err := store.Resolve(raw); err == nil {
			t.Errorf("Resolve(%q) succeeded, want a validation error", raw)
		}
	}
}
