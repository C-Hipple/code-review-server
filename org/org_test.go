package org

import (
	"testing"
)

func TestOrgItem_RepoAndIdentifier(t *testing.T) {
	tests := []struct {
		name               string
		details            []string
		expectedRepo       string
		expectedID         string
		expectedIdentifier string
	}{
		{
			name: "Standard repo and ID",
			details: []string{
				"123",
				"Repo: owner/repo",
				"http://github.com/owner/repo/pull/123",
			},
			expectedRepo:       "owner/repo",
			expectedID:         "123",
			expectedIdentifier: "owner/repo-123",
		},
		{
			name: "Repo with extra spaces",
			details: []string{
				" 456 ",
				"Repo:  my-org/my-repo  ",
			},
			expectedRepo:       "my-org/my-repo",
			expectedID:         "456",
			expectedIdentifier: "my-org/my-repo-456",
		},
		{
			name: "Missing repo",
			details: []string{
				"789",
				"Some other line",
			},
			expectedRepo:       "",
			expectedID:         "789",
			expectedIdentifier: "-789",
		},
		{
			name: "Empty details",
			details:            []string{},
			expectedRepo:       "",
			expectedID:         "",
			expectedIdentifier: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oi := OrgItem{
				details: tt.details,
			}
			if repo := oi.Repo(); repo != tt.expectedRepo {
				t.Errorf("Repo() = %v, want %v", repo, tt.expectedRepo)
			}
			if id := oi.ID(); id != tt.expectedID {
				t.Errorf("ID() = %v, want %v", id, tt.expectedID)
			}
			if identifier := oi.Identifier(); identifier != tt.expectedIdentifier {
				t.Errorf("Identifier() = %v, want %v", identifier, tt.expectedIdentifier)
			}
		})
	}
}
