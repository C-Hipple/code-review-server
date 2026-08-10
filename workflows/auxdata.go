package workflows

import (
	"sync"
)

// PRKey uniquely identifies a PR for aux data storage
type PRKey struct {
	Owner  string
	Repo   string
	Number int
}

// AuxDataStore holds pre-fetched auxiliary data for PRs
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
	data, ok := s.data[key]
	return data, ok
}

// Set stores pre-fetched aux data for a PR
func (s *AuxDataStore) Set(key PRKey, data *PRAuxData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = data
}

// MergeSet stores data for key, backfilling any fields data doesn't carry
// from the entry already stored. Post-filter fetches request only a subset of
// aux fields, and a plain Set would wipe whatever an earlier pass (e.g. the
// manager's pre-fetch) already gathered for the same PR.
func (s *AuxDataStore) MergeSet(key PRKey, data *PRAuxData) {
	if data == nil {
		return
	}
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
