package server

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"crs/images"
)

// ImageJSON is one image embedded in a pull request's description, reviews or
// comments, together with where the server put it on disk.
//
// Clients render bodies themselves, so they need a way to turn the URL in an
// <img> tag into bytes they can display. On a private repository they cannot
// fetch it: the attachment redirects to a signed URL that GitHub only issues
// to an authenticated request. Path is the answer — a local file every client
// can read, already downloaded with the server's token.
type ImageJSON struct {
	// URL is the image as it appears in the body.
	URL string `json:"url"`
	// Path is the cached file's absolute path, or "" when it isn't cached yet
	// — the prefetch is asynchronous, so a body can be served before its
	// images have landed. Clients that find an empty path can ask for the
	// image directly with RPCHandler.GetImage.
	Path string `json:"path"`
	// ContentType is the image's MIME type; empty alongside an empty Path.
	ContentType string `json:"content_type"`
	// Size is the cached file's size in bytes.
	Size int64 `json:"size"`
	// Cached is true when Path points at a file that exists right now.
	Cached bool `json:"cached"`
}

// collectImageURLs returns every GitHub-hosted image URL referenced anywhere in
// a PR's text, in the order a reader would meet them: description first, then
// reviews, then comments.
func collectImageURLs(details *PRDetails) []string {
	if details == nil {
		return nil
	}

	var urls []string
	seen := map[string]bool{}
	add := func(body string) {
		for _, u := range images.ExtractURLs(body) {
			if seen[u] {
				continue
			}
			seen[u] = true
			urls = append(urls, u)
		}
	}

	add(details.Metadata.Body)
	for _, r := range details.Reviews {
		add(r.Body)
	}
	for _, c := range details.Comments {
		add(c.Body)
	}
	for _, c := range details.OutdatedComments {
		add(c.Body)
	}
	return urls
}

// imagesForPR reports where each of a PR's images currently sits in the cache.
// It only reads the disk cache: serving a PR must never wait on a download.
func imagesForPR(details *PRDetails) []ImageJSON {
	urls := collectImageURLs(details)
	out := make([]ImageJSON, 0, len(urls))
	store := images.DefaultStore()
	for _, u := range urls {
		image := ImageJSON{URL: u}
		if entry, ok := store.Lookup(u); ok {
			image.Path = entry.Path
			image.ContentType = entry.ContentType
			image.Size = entry.Size
			image.Cached = true
		}
		out = append(out, image)
	}
	return out
}

// maxImageDataBytes caps the image GetImage sends inline (IncludeData). The
// whole reply is one JSON line, a third bigger again for base64, that both
// ends hold in memory; GitHub's attachments are well under it.
const maxImageDataBytes = 10 << 20

type GetImageArgs struct {
	// URL is the image URL as it appears in a PR body, review or comment.
	URL string `json:"URL"`
	// IncludeData asks for the image's bytes in the reply, for a client that
	// cannot read the server's disk: one connected to a hosted server, for
	// which Path names a file on another machine.
	IncludeData bool `json:"IncludeData"`
}

type GetImageReply struct {
	Okay        bool   `json:"okay"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	// Cached is false when this call had to download the image, which is the
	// case the prefetch exists to avoid.
	Cached bool   `json:"cached"`
	Error  string `json:"error"`
	// Data is the cached file's bytes (base64 in JSON) when IncludeData asked
	// for them, and absent otherwise.
	Data []byte `json:"data,omitempty"`
}

// GetImage returns the local path of an image embedded in a PR, downloading it
// first if the prefetch hasn't got to it yet.
//
// This is the fallback behind the `images` list in the PR payload: a client
// that meets an image with no path — a comment posted since the last fetch,
// say — asks for it here and gets a file it can render. A URL that isn't on a
// GitHub host comes back with okay:false, and the client should load it
// directly instead; those need no credentials.
//
// Unlike the rest of the read path this one can block on the network, because
// the caller is asking for exactly one image and has nothing to show without
// it.
//
// A remote client sets IncludeData and gets the bytes in the reply instead of
// a path it cannot open. With it, okay means the bytes came along: an image
// over maxImageDataBytes, or one whose file went away meanwhile, is okay:false
// with the reason in error and the rest of the reply filled in.
func (h *RPCHandler) GetImage(args *GetImageArgs, reply *GetImageReply) error {
	reply.URL = args.URL
	entry, cached, err := images.DefaultStore().Resolve(args.URL)
	if err != nil {
		slog.Warn("Could not serve an image embedded in a PR", "url", args.URL, "error", err)
		reply.Error = err.Error()
		return nil
	}
	reply.Okay = true
	reply.URL = entry.URL
	reply.Path = entry.Path
	reply.ContentType = entry.ContentType
	reply.Size = entry.Size
	reply.Cached = cached
	if args.IncludeData {
		data, err := readImageData(entry.Path)
		if err != nil {
			slog.Warn("Could not send an image's bytes", "url", entry.URL, "error", err)
			reply.Okay = false
			reply.Error = err.Error()
			return nil
		}
		reply.Data = data
	}
	return nil
}

// readImageData reads a cached image for GetImage to send inline, refusing
// one over maxImageDataBytes. It reads at most one byte past the cap rather
// than trusting the size a stat saw: cached files are replaced and deleted
// under the store's feet.
func readImageData(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not read the cached image: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageDataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read the cached image: %w", err)
	}
	if len(data) > maxImageDataBytes {
		return nil, fmt.Errorf("the image is over the %d MiB GetImage sends inline; open its URL on GitHub instead", maxImageDataBytes>>20)
	}
	return data, nil
}

// prefetchPRImages downloads the images a PR references, so that by the time
// anyone opens the review they are already on disk. Runs in its own goroutine
// and returns immediately; images that are already cached cost a stat each.
func prefetchPRImages(repo string, number int, details *PRDetails) {
	urls := collectImageURLs(details)
	if len(urls) == 0 {
		return
	}
	go func() {
		slog.Debug("Caching images embedded in a PR", "repo", repo, "pr", number, "count", len(urls))
		images.DefaultStore().Prefetch(urls)
	}()
}
