package images

import (
	"reflect"
	"testing"
)

// The shape GitHub's upload widget writes into a comment when someone pastes a
// screenshot, taken from a real review thread.
const commentWithScreenshots = `So I've done some quick debugging.

Mid race: <img width="1905" height="1189" alt="Screenshot 2025-12-01 at 13 04 37" src="https://github.com/user-attachments/assets/9b506449-0b9b-4c14-90f7-775beb9e68af" />

End of race: <img width="1910" height="1190" alt="Screenshot 2025-12-01 at 13 05 28" src="https://github.com/user-attachments/assets/016b4b0e-57d4-4b22-8ae9-34d730246647" />`

func TestExtractURLsFindsAttachedScreenshots(t *testing.T) {
	got := ExtractURLs(commentWithScreenshots)
	want := []string{
		"https://github.com/user-attachments/assets/9b506449-0b9b-4c14-90f7-775beb9e68af",
		"https://github.com/user-attachments/assets/016b4b0e-57d4-4b22-8ae9-34d730246647",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractURLs() = %v, want %v", got, want)
	}
}

func TestExtractURLsHandlesTheOtherWaysImagesAppear(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "markdown image syntax",
			body: "![shot](https://user-images.githubusercontent.com/1/x.png)",
			want: []string{"https://user-images.githubusercontent.com/1/x.png"},
		},
		{
			name: "markdown image with a title",
			body: `![shot](https://user-images.githubusercontent.com/1/x.png "a title")`,
			want: []string{"https://user-images.githubusercontent.com/1/x.png"},
		},
		{
			name: "single-quoted src",
			body: "<img src='https://github.com/user-attachments/assets/a'>",
			want: []string{"https://github.com/user-attachments/assets/a"},
		},
		{
			name: "unquoted src",
			body: "<img src=https://github.com/user-attachments/assets/a>",
			want: []string{"https://github.com/user-attachments/assets/a"},
		},
		{
			name: "uppercase tag",
			body: `<IMG SRC="https://github.com/user-attachments/assets/a">`,
			want: []string{"https://github.com/user-attachments/assets/a"},
		},
		{
			name: "light and dark picture sources",
			body: `<picture>
<source media="(prefers-color-scheme: dark)" srcset="https://github.com/user-attachments/assets/dark 1x, https://github.com/user-attachments/assets/dark2 2x">
<img src="https://github.com/user-attachments/assets/light">
</picture>`,
			want: []string{
				"https://github.com/user-attachments/assets/dark",
				"https://github.com/user-attachments/assets/dark2",
				"https://github.com/user-attachments/assets/light",
			},
		},
		{
			name: "escaped ampersands in a query string",
			body: `<img src="https://private-user-images.githubusercontent.com/1/x.png?jwt=abc&amp;v=2">`,
			want: []string{"https://private-user-images.githubusercontent.com/1/x.png?jwt=abc&v=2"},
		},
		{
			name: "the same image twice",
			body: `<img src="https://github.com/user-attachments/assets/a"> and again <img src="https://github.com/user-attachments/assets/a">`,
			want: []string{"https://github.com/user-attachments/assets/a"},
		},
		{
			name: "no images at all",
			body: "just some prose about a `<img>` tag",
			want: nil,
		},
		{
			name: "empty body",
			body: "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractURLs(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractURLs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractURLsSkipsWhatTheServerWillNotFetch(t *testing.T) {
	// A body is written by whoever commented on the PR, so anything off
	// GitHub is left for the client to load directly rather than becoming a
	// request this server makes.
	body := `<img src="https://img.shields.io/badge/ci-green.svg">
<img src="http://192.168.1.1/admin">
<img src="file:///etc/passwd">
<img src="data:image/png;base64,AAAA">
<img src="/relative/path.png">
<img src="https://github.com.evil.test/x.png">
<img src="https://githubusercontent.com.evil.test/x.png">
<img src="https://github.com/user-attachments/assets/keeper">`

	want := []string{"https://github.com/user-attachments/assets/keeper"}
	if got := ExtractURLs(body); !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractURLs() = %v, want %v", got, want)
	}
}

func TestIsGitHubImageHost(t *testing.T) {
	allowed := []string{
		"github.com",
		"GitHub.com",
		"www.github.com",
		"githubusercontent.com",
		"user-images.githubusercontent.com",
		"private-user-images.githubusercontent.com",
		"raw.githubusercontent.com",
	}
	for _, host := range allowed {
		if !IsGitHubImageHost(host) {
			t.Errorf("IsGitHubImageHost(%q) = false, want true", host)
		}
	}

	// Suffix matching has to be on a dot boundary.
	denied := []string{"notgithub.com", "githubusercontent.com.evil.test", "github.com.evil.test", "evil.test", ""}
	for _, host := range denied {
		if IsGitHubImageHost(host) {
			t.Errorf("IsGitHubImageHost(%q) = true, want false", host)
		}
	}
}
