package workflows

import (
	"strings"
	"sync"
)

// PRKey uniquely identifies a PR for aux data storage
type PRKey struct {
	Owner  string
	Repo   string
	Number int
}

// folded is key with its owner and repository lower-cased. GitHub tells
// neither apart by case, and one PR reaches the maps keyed by PRKey in more
// than one spelling: a cycle keys it by its repository as the config spells
// it, the filters and webhook deliveries by GitHub's own spelling, from the
// PR. A map that must find the one entry under either keys by this, as the DB
// caches fold theirs.
func (key PRKey) folded() PRKey {
	return PRKey{Owner: strings.ToLower(key.Owner), Repo: strings.ToLower(key.Repo), Number: key.Number}
}

// AuxDataStore holds pre-fetched auxiliary data for PRs, whatever the case of
// the key it is stored or asked for under (PRKey.folded).
type AuxDataStore struct {
	mu   sync.RWMutex
	data map[PRKey]*PRAuxData
}

// NewAuxDataStore creates a new AuxDataStore
func NewAuxDataStore() *AuxDataStore {
	return &AuxDataStore{
		data: make(map[PRKey]*PRAuxData),
	}
}

// Get retrieves pre-fetched aux data for a PR
func (s *AuxDataStore) Get(key PRKey) (*PRAuxData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.data[key.folded()]
	return data, ok
}

// Set stores pre-fetched aux data for a PR
func (s *AuxDataStore) Set(key PRKey, data *PRAuxData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key.folded()] = data
}

// MergeSet stores data for key, backfilling any fields data doesn't carry
// from the entry already stored. Post-filter fetches request only a subset of
// aux fields, and a plain Set would wipe whatever an earlier pass (e.g. the
// manager's pre-fetch) already gathered for the same PR.
func (s *AuxDataStore) MergeSet(key PRKey, data *PRAuxData) {
	if data == nil {
		return
	}
	key = key.folded()
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.data[key]; old != nil {
		if data.Comments == nil {
			data.Comments = old.Comments
			data.FormattedComments = old.FormattedComments
			data.CommentsCount = old.CommentsCount
		}
		if data.CIStatus == nil {
			data.CIStatus = old.CIStatus
		}
		if data.Diff == "" {
			data.Diff = old.Diff
			data.DiffLines = old.DiffLines
		}
		if data.Reviews == nil {
			data.Reviews = old.Reviews
		}
		if data.ReviewThreads == nil {
			data.ReviewThreads = old.ReviewThreads
		}
		if data.TeamReviews == nil {
			data.TeamReviews = old.TeamReviews
		}
		if data.Reactions == nil {
			data.Reactions = old.Reactions
		}
	}
	s.data[key] = data
}

// Global accessor for PRToOrgBridge to use
var currentAuxDataStore *AuxDataStore
var auxDataMu sync.RWMutex

// SetCurrentAuxDataStore sets the global aux data store for the current run
func SetCurrentAuxDataStore(store *AuxDataStore) {
	auxDataMu.Lock()
	defer auxDataMu.Unlock()
	currentAuxDataStore = store
}

// GetCurrentAuxDataStore retrieves the global aux data store
func GetCurrentAuxDataStore() *AuxDataStore {
	auxDataMu.RLock()
	defer auxDataMu.RUnlock()
	return currentAuxDataStore
}
