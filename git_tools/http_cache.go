package git_tools

import (
	"bytes"
	"container/list"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// GitHub's REST API tags each GET reply with an ETag (and usually a
// Last-Modified) naming that exact version of the resource. Asked again with
// those validators, GitHub answers 304 Not Modified if nothing has changed, and
// a 304 isn't charged against the rate limit. So a client that keeps what it
// was last sent can ask about a PR's comments, reviews and diff as often as it
// likes and pay only for what changed. GraphQL has no equivalent — every query
// is a POST, charged whether or not anything changed — which is why the
// lookups both APIs can answer default to REST (routing.go).
//
// The cache never answers on its own. Every request still goes to GitHub,
// carrying the validators of the copy held here, and only GitHub's 304 lets
// that copy stand in for the reply. Nothing stale is ever served: the saving
// is in the rate limit, not the round trip.

const (
	// responseCacheMaxBytes bounds the bodies held across every entry. A
	// cycle revalidates a few hundred replies, most of them a few kilobytes;
	// diffs are the big ones.
	responseCacheMaxBytes = 64 << 20
	// responseCacheMaxEntryBytes is the largest body kept. A bigger one — the
	// diff of a huge PR — passes through uncached rather than evicting a
	// hundred small entries to make room for itself.
	responseCacheMaxEntryBytes = 8 << 20
	// responseCacheEntryOverhead approximates what an entry's key and headers
	// cost on top of its body, so a cache of tiny bodies is bounded too.
	responseCacheEntryOverhead = 1 << 10
)

// globalResponseCache is shared by every REST client: GetGithubClient builds a
// new one per call, and the cache has to outlive them.
var globalResponseCache = newResponseCache(responseCacheMaxBytes, responseCacheMaxEntryBytes)

// HTTPCacheStats counts the REST response cache's work since the process
// started, and what it holds now.
type HTTPCacheStats struct {
	// NotModified is how many requests GitHub answered 304: replies served
	// from the cache, none of them charged against the rate limit.
	NotModified int64
	// Stored is how many replies have been added to the cache or have
	// replaced an older copy there.
	Stored  int64
	Entries int
	Bytes   int64
}

// GetHTTPCacheStats reports the REST response cache's counters.
func GetHTTPCacheStats() HTTPCacheStats {
	return globalResponseCache.stats()
}

// cachedResponse is a 200 reply kept for revalidation. It is never modified
// once stored, so its body can be handed out to concurrent readers.
type cachedResponse struct {
	key          string
	etag         string
	lastModified string
	header       http.Header
	body         []byte
}

func (e *cachedResponse) size() int64 {
	return int64(len(e.body)) + responseCacheEntryOverhead
}

// headersOfTheBody describe the stored body, so a 304's copies of them — if it
// sends any — are not carried over onto it.
var headersOfTheBody = map[string]bool{
	"Content-Encoding":  true,
	"Content-Length":    true,
	"Content-Type":      true,
	"Etag":              true,
	"Last-Modified":     true,
	"Transfer-Encoding": true,
}

// replyTo turns GitHub's 304 for req into the cached reply. The 304's own
// headers — its rate limit reading above all — replace the stored ones, except
// those describing the body, which is the stored one.
func (e *cachedResponse) replyTo(req *http.Request, notModified *http.Response) *http.Response {
	header := e.header.Clone()
	for name, values := range notModified.Header {
		if !headersOfTheBody[name] {
			header[name] = values
		}
	}
	// go-github leaves its own rate limit reading alone for a reply marked
	// this way; the transport under this one has already recorded the 304's.
	header.Set("X-From-Cache", "1")
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         notModified.Proto,
		ProtoMajor:    notModified.ProtoMajor,
		ProtoMinor:    notModified.ProtoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// responseCache holds replies by request, evicting the least recently used
// once the bodies outgrow maxBytes.
type responseCache struct {
	maxBytes      int64
	maxEntryBytes int64

	mu      sync.Mutex
	entries map[string]*list.Element // each holding a *cachedResponse
	lru     *list.List               // most recently used at the front
	bytes   int64

	notModified atomic.Int64
	stored      atomic.Int64
}

func newResponseCache(maxBytes, maxEntryBytes int64) *responseCache {
	return &responseCache{
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		entries:       map[string]*list.Element{},
		lru:           list.New(),
	}
}

// responseCacheKey identifies the reply a request asks for. Accept is part of
// it because GitHub serves different representations at one URL — a PR as
// JSON, or as a diff. The token is not: GitHub answers 304 only when the reply
// it would send is the version the validators name, whoever is asking.
func responseCacheKey(req *http.Request) string {
	return req.Header.Get("Accept") + " " + req.URL.String()
}

// revalidatable reports whether the cache handles req: a GET carrying no
// validators or range of its caller's own.
func revalidatable(req *http.Request) bool {
	return req.Method == http.MethodGet &&
		req.Header.Get("If-None-Match") == "" &&
		req.Header.Get("If-Modified-Since") == "" &&
		req.Header.Get("Range") == ""
}

func (c *responseCache) get(key string) *cachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.lru.MoveToFront(el)
	return el.Value.(*cachedResponse)
}

func (c *responseCache) put(entry *cachedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[entry.key]; ok {
		c.bytes -= el.Value.(*cachedResponse).size()
		c.lru.Remove(el)
	}
	c.entries[entry.key] = c.lru.PushFront(entry)
	c.bytes += entry.size()
	for c.bytes > c.maxBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		evicted := c.lru.Remove(oldest).(*cachedResponse)
		delete(c.entries, evicted.key)
		c.bytes -= evicted.size()
	}
	c.stored.Add(1)
}

// store keeps a 200 reply that carries validators and returns a response that
// reads the same body. A reply with no validators, one marked no-store, or one
// too big to keep passes through untouched.
func (c *responseCache) store(key string, resp *http.Response) (*http.Response, error) {
	etag, lastModified := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if (etag == "" && lastModified == "") || strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		return resp, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxEntryBytes+1))
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	if int64(len(body)) > c.maxEntryBytes {
		// Too big to keep: hand back what has been read, then the rest.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return resp, nil
	}
	resp.Body.Close()

	c.put(&cachedResponse{
		key:          key,
		etag:         etag,
		lastModified: lastModified,
		header:       resp.Header.Clone(),
		body:         body,
	})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

func (c *responseCache) stats() HTTPCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return HTTPCacheStats{
		NotModified: c.notModified.Load(),
		Stored:      c.stored.Load(),
		Entries:     len(c.entries),
		Bytes:       c.bytes,
	}
}

// revalidatingRoundTripper sends each GET to GitHub with the validators of the
// reply cached for it, and turns GitHub's 304 into that reply.
type revalidatingRoundTripper struct {
	next  http.RoundTripper
	cache *responseCache
}

func (t *revalidatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !revalidatable(req) {
		return t.next.RoundTrip(req)
	}

	key := responseCacheKey(req)
	cached := t.cache.get(key)
	sent := req
	if cached != nil {
		// A RoundTripper must leave its caller's request alone.
		sent = req.Clone(req.Context())
		if cached.etag != "" {
			sent.Header.Set("If-None-Match", cached.etag)
		} else {
			sent.Header.Set("If-Modified-Since", cached.lastModified)
		}
	}

	resp, err := t.next.RoundTrip(sent)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && cached != nil:
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		t.cache.notModified.Add(1)
		slog.Debug("GitHub reply unchanged; serving the cached copy", "url", req.URL.String())
		return cached.replyTo(req, resp), nil
	case resp.StatusCode == http.StatusOK:
		return t.cache.store(key, resp)
	}
	return resp, nil
}
