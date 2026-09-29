package server

import (
	"crs/ai"
	"crs/config"
	"crs/database"
	"crs/utils"
	"encoding/json"
	"log/slog"
)

// The applied AI features (ai.AppliedFeature) have no report for a client to
// open: the server reads their stored results into what it already serves.
// file-ordering orders the files of the diff GetPR returns, and review-ease
// rates the PR in the review list, the org headlines and the PR's metadata.
// The reads below never start a run and never wait for one; runs are asked
// for when a client opens the PR (ensurePostUpdateHooks), and run
// automatically after a workflow fetches or updates it when config says so.
//
// Orderings and ratings computed before these were AI features live in the
// DiffFileOrderingCache table, which nothing writes any more. A PR with no
// usable stored result falls back to it, so they keep showing.

// aiFeatureEnabled reports whether config switches the AI feature id on.
func aiFeatureEnabled(id string) bool {
	entry, _ := config.C().AIFeatureSettings(id)
	return entry.Enabled
}

// orderDiffFiles returns the diff files in display order: the order
// file-ordering stored for the revision sha when the feature is on and has
// one, and otherwise the default sort, test files last.
//
// Rendering never waits on a model. A PR opened before its order is stored
// shows the default sort, and the order from the next render of the same
// revision on: blocking here would put a multi-second model call in front of
// opening a review, which is precisely the delay this path must not have.
func orderDiffFiles(files []*utils.DiffFile, owner, repo string, prNumber int, sha string) []*utils.DiffFile {
	if !aiFeatureEnabled(ai.FileOrderingID) {
		return sortFilesTestsLast(files)
	}
	if len(files) < 2 {
		return files
	}
	if names := storedFileOrdering(owner, repo, prNumber, sha); names != nil {
		return ai.OrderDiffFiles(files, names)
	}
	return sortFilesTestsLast(files)
}

// storedFileOrdering returns the file order stored for the PR at sha, or nil
// when there is none for that revision.
func storedFileOrdering(owner, repo string, prNumber int, sha string) []string {
	db := config.C().DB
	if db == nil || sha == "" {
		return nil
	}
	stored, ok, err := db.GetAIResult(owner, repo, prNumber, ai.FileOrderingID)
	if err != nil {
		slog.Warn("Error reading the stored file ordering", "repo", repo, "pr", prNumber, "error", err)
	}
	if ok && stored.SHA == sha && stored.Status == ai.StatusSuccess {
		if names := ai.StoredFileOrdering(stored.Result); names != nil {
			return names
		}
	}
	return legacyFileOrdering(db, repo, prNumber, sha)
}

// legacyFileOrdering reads an order computed for the PR at sha before
// file-ordering was an AI feature.
func legacyFileOrdering(db *database.DB, repo string, prNumber int, sha string) []string {
	encoded, err := db.GetDiffFileOrdering(prNumber, repo, sha)
	if err != nil {
		slog.Warn("Error reading the legacy diff file ordering", "repo", repo, "pr", prNumber, "error", err)
		return nil
	}
	if encoded == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(encoded), &names); err != nil || len(names) == 0 {
		return nil
	}
	return names
}

// reviewEase returns the rating to show for a PR — easy, medium or hard — or
// "" when review-ease is off or has rated nothing. It is the latest rating
// stored, whichever revision it rated: after a push, the PR keeps its rating
// until the new head's is in.
func reviewEase(owner, repo string, number int) string {
	if repo == "" || number <= 0 || !aiFeatureEnabled(ai.ReviewEaseID) {
		return ""
	}
	db := config.C().DB
	if db == nil {
		return ""
	}
	stored, ok, err := db.GetAIResult(owner, repo, number, ai.ReviewEaseID)
	if err != nil {
		slog.Warn("Error reading the stored review ease", "repo", repo, "pr", number, "error", err)
	}
	if ok && stored.Status == ai.StatusSuccess {
		if ease := ai.StoredReviewEase(stored.Result); ease != "" {
			return ease
		}
	}
	// Nothing rated yet, or the latest run failed: fall back to a rating from
	// before review-ease was an AI feature.
	ease, err := db.GetLatestReviewEase(number, repo)
	if err != nil {
		slog.Warn("Error reading the legacy review ease", "repo", repo, "pr", number, "error", err)
		return ""
	}
	return ease
}
