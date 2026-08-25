// Package images downloads and caches the images embedded in a pull request's
// description, reviews and comments.
//
// Screenshots are how reviewers explain a bug, and every one of them is a
// problem for a client on its own: an attachment URL redirects to a signed CDN
// URL that GitHub only issues to an authenticated request, so on a private
// repository the bytes are unreachable without the server's token. Doing the
// fetch here means the token stays in one place and every client — web, Emacs,
// anything else — renders the same images from local disk.
package images

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// Bodies arrive as GitHub-flavored markdown, where an attached screenshot is
// usually a raw <img> tag (that is what the upload widget writes) but can also
// be markdown image syntax, or a <picture>/<source> pair for a light/dark
// screenshot.
var (
	imageTagPattern  = regexp.MustCompile(`(?is)<(?:img|source)\b[^>]*>`)
	srcAttrPattern   = regexp.MustCompile(`(?is)\b(src|srcset)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	markdownImgRegex = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^)\s>]+)>?[^)]*\)`)
)

// IsGitHubImageHost reports whether a host is one GitHub serves attachments and
// repository content from.
//
// Only these are cached. A body is written by whoever commented on the pull
// request, so treating every URL in it as something to fetch would turn the
// review server into a request generator pointed wherever a commenter likes.
// Images hosted elsewhere (CI badges, say) are left for the client to load
// directly, which needs no credentials anyway.
func IsGitHubImageHost(host string) bool {
	h := strings.ToLower(host)
	return h == "github.com" ||
		h == "www.github.com" ||
		h == "githubusercontent.com" ||
		strings.HasSuffix(h, ".githubusercontent.com")
}

// ParseURL returns the parsed form of an image URL the server is willing to
// fetch, or nil when it is unparseable, not http(s), or not on a GitHub host.
func ParseURL(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil
	}
	if !IsGitHubImageHost(parsed.Hostname()) {
		return nil
	}
	return parsed
}

// ExtractURLs returns the GitHub-hosted image URLs referenced by a body, in the
// order they appear and without duplicates.
func ExtractURLs(body string) []string {
	if body == "" {
		return nil
	}

	var found []string
	seen := make(map[string]bool)
	add := func(raw string) {
		// Attribute values are HTML-escaped, so a URL with query parameters
		// arrives with &amp; where the server needs &.
		raw = html.UnescapeString(strings.TrimSpace(raw))
		parsed := ParseURL(raw)
		if parsed == nil || seen[parsed.String()] {
			return
		}
		seen[parsed.String()] = true
		found = append(found, parsed.String())
	}

	for _, tag := range imageTagPattern.FindAllString(body, -1) {
		for _, attr := range srcAttrPattern.FindAllStringSubmatch(tag, -1) {
			value := firstNonEmpty(attr[2], attr[3], attr[4])
			if strings.EqualFold(attr[1], "srcset") {
				for _, candidate := range splitSrcSet(value) {
					add(candidate)
				}
				continue
			}
			add(value)
		}
	}

	for _, match := range markdownImgRegex.FindAllStringSubmatch(body, -1) {
		add(match[1])
	}

	return found
}

// splitSrcSet pulls the URLs out of a srcset attribute, whose candidates are
// "<url> [descriptor]" separated by commas.
func splitSrcSet(value string) []string {
	var urls []string
	for _, candidate := range strings.Split(value, ",") {
		fields := strings.Fields(candidate)
		if len(fields) > 0 {
			urls = append(urls, fields[0])
		}
	}
	return urls
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
