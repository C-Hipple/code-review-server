package server

import (
	"crs/config"
	"errors"
	"strings"
	"testing"
)

// Who may write to GitHub under the server's token: the local connection
// always; a remote one only where the server allows it, and a merge then only
// for an administrator. A remote user the server could link is told to link;
// the administrator token's session, which has no account to link, is not.
// (Linked users' writes: github_link_test.go.)
func TestWriteClientFor(t *testing.T) {
	aiTestSetup(t, nil)
	t.Setenv(secretKeyEnv, testSecretKey)
	local, user, admin := &RPCHandler{}, handlerFor("alice", false), handlerFor("", true)

	for _, tc := range []struct {
		name     string
		h        *RPCHandler
		allow    bool
		linkable bool
		action   writeAction
		want     error
	}{
		{"local submit", local, false, false, writeSubmitReview, nil},
		{"local merge", local, false, true, writeMerge, nil},
		{"user submit", user, false, false, writeSubmitReview, errServerIdentityRefused},
		{"user merge", user, false, false, writeMerge, errServerIdentityRefused},
		{"administrator merge", admin, false, false, writeMerge, errServerIdentityRefused},
		{"user submit, allowed", user, true, false, writeSubmitReview, nil},
		{"user merge, allowed", user, true, false, writeMerge, errMergeAsServerNeedsAdmin},
		{"administrator merge, allowed", admin, true, false, writeMerge, nil},
		{"user submit, linkable", user, false, true, writeSubmitReview, errLinkRequired},
		{"user merge, linkable", user, false, true, writeMerge, errLinkRequired},
		{"administrator merge, linkable", admin, false, true, writeMerge, errServerIdentityRefused},
		{"user submit, linkable and allowed", user, true, true, writeSubmitReview, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.C()
			cfg.AllowServerIdentityActions = tc.allow
			cfg.GitHubOAuthClientID = ""
			if tc.linkable {
				cfg.GitHubOAuthClientID = "Iv1.client"
			}
			config.SetC(cfg)
			identity, err := tc.h.writeClientFor(tc.h.viewer(), tc.action)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if (identity != nil) != (tc.want == nil) {
				t.Errorf("identity = %v with err %v", identity, err)
			}
			if identity != nil && (identity.client == nil || identity.sealed != "") {
				t.Errorf("an unlinked write's identity = %+v, want the server's client", identity)
			}
		})
	}

	// The prefixes clients match on.
	if !strings.HasPrefix(errServerIdentityRefused.Error(), "refused: ") ||
		!strings.HasPrefix(errMergeAsServerNeedsAdmin.Error(), "permission denied: ") {
		t.Errorf("refusals %q and %q lack the prefixes clients match on", errServerIdentityRefused, errMergeAsServerNeedsAdmin)
	}
}

// SkipCache refetches everything on the shared token, so it is an
// administrator's; the cached read stays open to users.
func TestSkipCacheNeedsAdmin(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	useFakeGitHub(t)
	seedSectionItem(t, db, "alice", "Needs Review", aiNumber, aiAt(0))

	alice := handlerFor("alice", false)
	err := alice.GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, SkipCache: true}, &GetPRReply{})
	if err == nil || err.Error() != "permission denied: SkipCache requires an administrator" {
		t.Errorf("a user's GetPR with SkipCache: err = %v", err)
	}
	err = alice.GetAdjacentPR(&GetAdjacentPRArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, SkipCache: true}, &GetAdjacentPRReply{})
	if err == nil || err.Error() != "permission denied: SkipCache requires an administrator" {
		t.Errorf("a user's GetAdjacentPR with SkipCache: err = %v", err)
	}
	if err := alice.GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &GetPRReply{}); err != nil {
		t.Errorf("a user's cached GetPR: %v", err)
	}

	for name, h := range map[string]*RPCHandler{"local": {}, "remote administrator": handlerFor("", true)} {
		if err := h.GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, SkipCache: true}, &GetPRReply{}); err != nil {
			t.Errorf("%s GetPR with SkipCache: %v", name, err)
		}
	}
}

// GetHunkContext reads the server's clones by repository name; a remote
// client cannot hand it a path.
func TestRemoteHunkContextTakesOnlyRepositoryNames(t *testing.T) {
	aiTestSetup(t, nil)
	alice := handlerFor("alice", false)
	for _, repo := range []string{"../etc", "a/b", `a\b`, "..", ""} {
		err := alice.GetHunkContext(&GetHunkContextArgs{Owner: aiOwner, Repo: repo, Number: aiNumber,
			Filename: "x", Side: "new", AnchorLine: 1, Direction: "after"}, &GetHunkContextReply{})
		if err == nil || !strings.Contains(err.Error(), "Repo must be a repository name") {
			t.Errorf("GetHunkContext(Repo %q): err = %v", repo, err)
		}
	}
}
