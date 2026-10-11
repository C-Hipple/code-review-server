package config

import "testing"

func TestOwnershipKey(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		workflow string
		want     string
	}{
		// The server's own workflows keep their bare name, so items written
		// before users existed still match them.
		{"server workflow", "", "review_requests", "review_requests"},
		{"user workflow", "octocat", "review_requests", "octocat\x1freview_requests"},
		{"empty name", "octocat", "", "octocat\x1f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OwnershipKey(tt.user, tt.workflow); got != tt.want {
				t.Errorf("OwnershipKey(%q, %q) = %q, want %q", tt.user, tt.workflow, got, tt.want)
			}
		})
	}
}

// Two users' workflows of the same name, and the server's, must never share a
// key: that is what keeps one dashboard from releasing another's items.
func TestOwnershipKeyDistinguishesUsers(t *testing.T) {
	keys := map[string]bool{}
	for _, user := range []string{"", "alice", "bob"} {
		key := OwnershipKey(user, "review_requests")
		if keys[key] {
			t.Fatalf("OwnershipKey(%q, %q) = %q collides with another user's", user, "review_requests", key)
		}
		keys[key] = true
	}
}

func TestSplitOwnershipKeyRoundTrips(t *testing.T) {
	tests := []struct {
		user, name string
	}{
		{"", "review_requests"},
		{"", ""},
		{"octocat", "review_requests"},
		{"octo-cat", "needs review"},
		// Only the first separator splits: a login cannot hold one, so
		// anything after it belongs to the name.
		{"octocat", "odd\x1fname"},
	}
	for _, tt := range tests {
		user, name := SplitOwnershipKey(OwnershipKey(tt.user, tt.name))
		if user != tt.user || name != tt.name {
			t.Errorf("SplitOwnershipKey(OwnershipKey(%q, %q)) = (%q, %q)", tt.user, tt.name, user, name)
		}
	}
}

// Keys stored before users existed are bare workflow names: they split as the
// server's own.
func TestSplitOwnershipKeyBareName(t *testing.T) {
	user, name := SplitOwnershipKey("team_reviews")
	if user != "" || name != "team_reviews" {
		t.Errorf("SplitOwnershipKey(%q) = (%q, %q), want (%q, %q)", "team_reviews", user, name, "", "team_reviews")
	}
}
