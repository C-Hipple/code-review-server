package server

import (
	"reflect"
	"testing"
	"time"

	"crs/images"
)

// A screenshot as GitHub's upload widget writes it into a comment.
const screenshotTag = `<img width="1905" height="1189" alt="Screenshot" src="https://github.com/user-attachments/assets/9b506449" />`

func TestCollectImageURLsReadsEveryBodyOnThePR(t *testing.T) {
	details := &PRDetails{
		Metadata: PRMetadata{Body: "Here's the before shot: " + screenshotTag},
		Reviews: []ReviewJSON{
			{Body: `<img src="https://github.com/user-attachments/assets/review">`},
		},
		Comments: []CommentJSON{
			{Body: "![diagram](https://user-images.githubusercontent.com/1/diagram.png)"},
			// The same screenshot quoted back in a reply is not a second image.
			{Body: screenshotTag},
		},
		OutdatedComments: []CommentJSON{
			{Body: `<img src="https://github.com/user-attachments/assets/outdated">`},
		},
	}

	got := collectImageURLs(details)
	want := []string{
		"https://github.com/user-attachments/assets/9b506449",
		"https://github.com/user-attachments/assets/review",
		"https://user-images.githubusercontent.com/1/diagram.png",
		"https://github.com/user-attachments/assets/outdated",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectImageURLs() = %v, want %v", got, want)
	}
}

func TestCollectImageURLsOnAPRWithoutImages(t *testing.T) {
	details := &PRDetails{
		Metadata: PRMetadata{Body: "no images here"},
		Comments: []CommentJSON{{Body: "still none"}},
	}
	if got := collectImageURLs(details); got != nil {
		t.Errorf("collectImageURLs() = %v, want nil", got)
	}
	if got := collectImageURLs(nil); got != nil {
		t.Errorf("collectImageURLs(nil) = %v, want nil", got)
	}
}

// seedImage caches bytes for a URL, standing in for an image the prefetch
// already downloaded.
func seedImage(t *testing.T, url string, body []byte) string {
	t.Helper()
	entry, err := images.DefaultStore().Put(url, "image/png", body)
	if err != nil {
		t.Fatalf("seeding the image cache: %v", err)
	}
	return entry.Path
}

func TestImagesForPRReportsWhatIsOnDisk(t *testing.T) {
	t.Setenv("CRS_HOME", t.TempDir())

	cachedURL := "https://github.com/user-attachments/assets/cached"
	missingURL := "https://github.com/user-attachments/assets/missing"
	path := seedImage(t, cachedURL, []byte("png-bytes"))

	details := &PRDetails{
		Metadata: PRMetadata{Body: `<img src="` + cachedURL + `"> and <img src="` + missingURL + `">`},
	}

	got := imagesForPR(details)
	if len(got) != 2 {
		t.Fatalf("imagesForPR() returned %d images, want 2", len(got))
	}

	if got[0].URL != cachedURL || !got[0].Cached {
		t.Errorf("cached image = %+v, want a cache hit for %s", got[0], cachedURL)
	}
	if got[0].Path != path {
		t.Errorf("cached image path = %q, want %q", got[0].Path, path)
	}
	if got[0].ContentType != "image/png" {
		t.Errorf("cached image content type = %q, want image/png", got[0].ContentType)
	}
	if got[0].Size != int64(len("png-bytes")) {
		t.Errorf("cached image size = %d, want %d", got[0].Size, len("png-bytes"))
	}

	// An image the prefetch hasn't reached yet is still listed, with an empty
	// path — that is the client's cue to ask for it with GetImage.
	if got[1].URL != missingURL || got[1].Cached || got[1].Path != "" {
		t.Errorf("uncached image = %+v, want an empty path for %s", got[1], missingURL)
	}
}

func TestGetImageServesACachedImageWithoutTheNetwork(t *testing.T) {
	t.Setenv("CRS_HOME", t.TempDir())
	t.Setenv("CRS_GITHUB_TOKEN", "")

	url := "https://github.com/user-attachments/assets/cached"
	path := seedImage(t, url, []byte("png-bytes"))

	handler := &RPCHandler{}
	reply := &GetImageReply{}
	done := make(chan error, 1)
	go func() { done <- handler.GetImage(&GetImageArgs{URL: url}, reply) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GetImage() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetImage() blocked on a cached image")
	}

	if !reply.Okay || !reply.Cached {
		t.Errorf("reply = %+v, want okay and cached", reply)
	}
	if reply.Path != path {
		t.Errorf("reply.Path = %q, want %q", reply.Path, path)
	}
	if reply.ContentType != "image/png" {
		t.Errorf("reply.ContentType = %q, want image/png", reply.ContentType)
	}
}

func TestGetImageRefusesAnythingOffGitHub(t *testing.T) {
	t.Setenv("CRS_HOME", t.TempDir())

	handler := &RPCHandler{}
	for _, url := range []string{
		"https://evil.test/pixel.png",
		"http://192.168.1.1/admin",
		"file:///etc/passwd",
	} {
		reply := &GetImageReply{}
		// A refusal is a soft failure, not an RPC error: the client just loads
		// the URL itself.
		if err := handler.GetImage(&GetImageArgs{URL: url}, reply); err != nil {
			t.Errorf("GetImage(%q) returned an RPC error: %v", url, err)
		}
		if reply.Okay || reply.Error == "" {
			t.Errorf("GetImage(%q) reply = %+v, want okay:false with a reason", url, reply)
		}
	}
}
