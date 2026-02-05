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
