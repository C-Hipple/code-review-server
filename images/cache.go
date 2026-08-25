package images

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crs/config"
)

const (
	// MaxImageBytes caps a single download. GitHub's own attachment limit is
	// well under this; the cap exists so a comment linking something enormous
	// can't fill the disk.
	MaxImageBytes = 25 << 20

	fetchTimeout    = 30 * time.Second
	maxRedirects    = 5
	prefetchWorkers = 4

	// How long a failed fetch is remembered, so a broken URL in an old comment
	// isn't retried on every pass over the pull request.
	failureTTL = 10 * time.Minute
)

// The extension is how a client learns an image's type: Emacs hands the cached
// file to shr, which types it by extension, and the web bridge serves it back
// with the matching content type. Ordered so lookups are deterministic.
var imageTypes = []struct {
	contentType string
	ext         string
}{
	{"image/png", ".png"},
	{"image/jpeg", ".jpg"},
	{"image/gif", ".gif"},
	{"image/webp", ".webp"},
	{"image/svg+xml", ".svg"},
	{"image/avif", ".avif"},
	{"image/apng", ".apng"},
	{"image/bmp", ".bmp"},
	{"image/x-icon", ".ico"},
	{"image/vnd.microsoft.icon", ".ico"},
	{"image/tiff", ".tiff"},
}

// Entry is a cached image on disk.
type Entry struct {
	// URL is the normalized source URL the bytes came from.
	URL string
	// Path is the absolute path of the cached file.
	Path string
	// ContentType is the image's MIME type, as recorded by its extension.
	ContentType string
	// Size is the file size in bytes.
	Size int64
}

type failure struct {
	err   error
	until time.Time
}

type pendingFetch struct {
	done  chan struct{}
	entry Entry
	err   error
}

// Store is a content-addressed cache of downloaded images. The zero value is
// not usable; construct one with NewStore or take the shared one from
// DefaultStore.
type Store struct {
	dir        string
	httpClient *http.Client
	// token is read per request so a token exported after startup still works.
	token func() string
	// allowHost gates which hosts may be fetched. Overridden in tests, which
	// serve from a local address the real allowlist rejects.
	allowHost func(string) bool

	mu       sync.Mutex
	inflight map[string]*pendingFetch
	failures map[string]failure
}

// NewStore returns a store that caches images under dir.
func NewStore(dir string) *Store {
	return &Store{
		dir: dir,
		httpClient: &http.Client{
			Timeout: fetchTimeout,
			// Redirects are followed by hand: every hop has to be re-checked
			// against the host allowlist, and the token has to be dropped
			// after the first one.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		token:     func() string { return os.Getenv("CRS_GITHUB_TOKEN") },
		allowHost: IsGitHubImageHost,
		inflight:  make(map[string]*pendingFetch),
		failures:  make(map[string]failure),
	}
}

var (
	storesMu sync.Mutex
	stores   = map[string]*Store{}
)

// DefaultStore returns the shared store under $CRS_HOME/images. One store per
// directory, so the in-flight and failure bookkeeping is shared by everything
// that asks for the same cache — and so a test that repoints CRS_HOME gets its
// own store rather than a stale one.
func DefaultStore() *Store {
	dir := defaultDir()
	storesMu.Lock()
	defer storesMu.Unlock()
	if s, ok := stores[dir]; ok {
		return s
	}
	s := NewStore(dir)
	stores[dir] = s
	return s
}

func defaultDir() string {
	home, err := config.GetCRSHome()
	if err != nil || home == "" {
		slog.Warn("Could not resolve CRS home for the image cache; using a temp directory", "error", err)
		return filepath.Join(os.TempDir(), "crs-images")
	}
	return filepath.Join(home, "images")
}

// Dir is the directory cached images are written to.
func (s *Store) Dir() string { return s.dir }

// Lookup reports whether an image is already on disk, without touching the
// network. Callers on the read path use this: serving a pull request must
// never wait on a download.
func (s *Store) Lookup(rawURL string) (Entry, bool) {
	key, err := s.normalize(rawURL)
	if err != nil {
		return Entry{}, false
	}
	return s.lookupKey(key)
}

func (s *Store) lookupKey(key string) (Entry, bool) {
	base := s.pathBase(key)
	for _, t := range imageTypes {
		path := base + t.ext
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		return Entry{URL: key, Path: path, ContentType: t.contentType, Size: info.Size()}, true
	}
	return Entry{}, false
}

// Resolve returns a cached image, downloading it first if it isn't cached yet.
// The bool reports whether the entry was already on disk, which is what the
// prefetch is trying to make true for everything a reviewer is about to see.
func (s *Store) Resolve(rawURL string) (Entry, bool, error) {
	key, err := s.normalize(rawURL)
	if err != nil {
		return Entry{}, false, err
	}
	if entry, ok := s.lookupKey(key); ok {
		return entry, true, nil
	}
	entry, err := s.fetch(key)
	return entry, false, err
}

// Put stores bytes the caller already has under an image's URL, as though they
// had been downloaded. Returns an error for a URL the store would not fetch,
// or a content type it has no extension for.
func (s *Store) Put(rawURL, contentType string, body []byte) (Entry, error) {
	key, err := s.normalize(rawURL)
	if err != nil {
		return Entry{}, err
	}
	normalized := normalizeContentType(contentType)
	ext, ok := extensionFor(normalized)
	if !ok {
		return Entry{}, fmt.Errorf("unsupported content type %q", contentType)
	}
	path := s.pathBase(key) + ext
	if err := s.write(path, body); err != nil {
		return Entry{}, err
	}
	return Entry{URL: key, Path: path, ContentType: normalized, Size: int64(len(body))}, nil
}

// Prefetch downloads any of these images that aren't cached yet. It blocks
// until they are all done, so callers run it in a goroutine; failures are
// logged rather than returned, since one broken image should not stop the
// rest.
func (s *Store) Prefetch(urls []string) {
	if len(urls) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, prefetchWorkers)
	fetched := 0
	for _, raw := range urls {
		key, err := s.normalize(raw)
		if err != nil {
			continue
		}
		if _, ok := s.lookupKey(key); ok {
			continue
		}
		fetched++
		wg.Add(1)
		sem <- struct{}{}
		go func(key string) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.fetch(key); err != nil {
				slog.Warn("Failed to cache an image embedded in a PR", "url", key, "error", err)
			}
		}(key)
	}
	wg.Wait()
	if fetched > 0 {
		slog.Info("Cached images embedded in a PR", "attempted", fetched, "referenced", len(urls))
	}
}

// fetch downloads one image, collapsing concurrent requests for the same URL
// into a single download.
func (s *Store) fetch(key string) (Entry, error) {
	s.mu.Lock()
	if f, ok := s.failures[key]; ok {
		if time.Now().Before(f.until) {
			s.mu.Unlock()
			return Entry{}, f.err
		}
		delete(s.failures, key)
	}
	if pending, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		<-pending.done
		return pending.entry, pending.err
	}
	pending := &pendingFetch{done: make(chan struct{})}
	s.inflight[key] = pending
	s.mu.Unlock()

	pending.entry, pending.err = s.download(key)

	s.mu.Lock()
	delete(s.inflight, key)
	if pending.err != nil {
		s.failures[key] = failure{err: pending.err, until: time.Now().Add(failureTTL)}
	}
	s.mu.Unlock()
	close(pending.done)

	return pending.entry, pending.err
}

func (s *Store) download(key string) (Entry, error) {
	resp, err := s.get(key)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Entry{}, fmt.Errorf("github returned %d for %s", resp.StatusCode, key)
	}

	// A sign-in page is a failure dressed as a 200, and writing non-image bytes
	// into the cache would hand every client something it can't render.
	contentType := normalizeContentType(resp.Header.Get("Content-Type"))
	ext, ok := extensionFor(contentType)
	if !ok {
		return Entry{}, fmt.Errorf("unsupported content type %q for %s", contentType, key)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	if err != nil {
		return Entry{}, fmt.Errorf("reading %s: %w", key, err)
	}
	if len(body) > MaxImageBytes {
		return Entry{}, fmt.Errorf("image at %s is larger than %d bytes", key, MaxImageBytes)
	}

	path := s.pathBase(key) + ext
	if err := s.write(path, body); err != nil {
		return Entry{}, err
	}
	return Entry{URL: key, Path: path, ContentType: contentType, Size: int64(len(body))}, nil
}

// get issues the request, following redirects by hand.
func (s *Store) get(key string) (*http.Response, error) {
	current := key
	token := s.token()

	for hop := 0; hop <= maxRedirects; hop++ {
		req, err := http.NewRequest(http.MethodGet, current, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "image/*")
		req.Header.Set("User-Agent", "code-review-server")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return resp, nil
		}

		location := resp.Header.Get("Location")
		resp.Body.Close()
		if location == "" {
			return nil, fmt.Errorf("redirect without a location from %s", current)
		}
		base, err := url.Parse(current)
		if err != nil {
			return nil, err
		}
		next, err := base.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("unparseable redirect from %s: %w", current, err)
		}
		if !s.allowHost(next.Hostname()) {
			return nil, fmt.Errorf("refusing to follow a redirect off GitHub to %s", next.Hostname())
		}
		current = next.String()
		// The signed URL GitHub redirects to carries its own credentials in the
		// query string, and the request is rejected outright if an
		// Authorization header rides along with it.
		token = ""
	}
	return nil, fmt.Errorf("too many redirects fetching %s", key)
}

// write puts the bytes in place atomically, so a download interrupted halfway
// never leaves a truncated image that later looks like a cache hit.
func (s *Store) write(path string, body []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("creating the image cache directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".partial-*")
	if err != nil {
		return fmt.Errorf("creating a temp file for %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	// CreateTemp makes the file private to the owner; images are read by
	// whatever client is rendering the review, so give them the usual mode.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("setting the mode on %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("moving the image into place at %s: %w", path, err)
	}
	return nil
}

// normalize validates a URL and returns the canonical string used as its cache
// key.
func (s *Store) normalize(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("unparseable image url %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("image url %q is not http(s)", raw)
	}
	if !s.allowHost(parsed.Hostname()) {
		return "", fmt.Errorf("image url %q is not on a GitHub host", raw)
	}
	return parsed.String(), nil
}

// pathBase is the cached file's path without its extension. Content-addressing
// by URL keeps names stable across restarts and free of anything a commenter
// could use to escape the cache directory.
func (s *Store) pathBase(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:]))
}

func normalizeContentType(header string) string {
	value, _, _ := strings.Cut(header, ";")
	return strings.ToLower(strings.TrimSpace(value))
}

func extensionFor(contentType string) (string, bool) {
	for _, t := range imageTypes {
		if t.contentType == contentType {
			return t.ext, true
		}
	}
	return "", false
}
