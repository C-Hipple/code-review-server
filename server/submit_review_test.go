package server

import (
	"crs/database"
	"testing"
)

func localComment(id int64, replyTo *int64) database.LocalComment {
	body := "pending"
	return database.LocalComment{ID: id, Body: &body, ReplyToID: replyTo}
}

func ptr(v int64) *int64 { return &v }

// TestResolveReplyAnchor covers the ids SubmitReview may hand GitHub. Local ids
// are row ids in our own database; sending one as in_reply_to gets the reply
// rejected, and the reviewer's text is lost with it.
func TestResolveReplyAnchor(t *testing.T) {
	tests := []struct {
		name    string
		comment database.LocalComment
		pending []database.LocalComment
		want    int64
		wantOK  bool
	}{
		{
			name:    "top-level comment is not a reply",
			comment: localComment(1, nil),
			want:    0,
		},
		{
			name:    "reply to a GitHub comment answers that comment",
			comment: localComment(1, ptr(2147483001)),
			want:    2147483001,
			wantOK:  true,
		},
		{
			// Replying twice to the same thread in one sitting: the second
			// reply targets the first, which only exists locally.
			name:    "reply to a pending reply answers the GitHub comment above it",
			comment: localComment(2, ptr(1)),
			pending: []database.LocalComment{localComment(1, ptr(2147483001))},
			want:    2147483001,
			wantOK:  true,
		},
		{
			name:    "chain of pending replies walks up to the GitHub comment",
			comment: localComment(3, ptr(2)),
			pending: []database.LocalComment{
				localComment(1, ptr(2147483001)),
				localComment(2, ptr(1)),
			},
			want:   2147483001,
			wantOK: true,
		},
		{
			// The comment being answered has not been submitted either, so
			// there is nothing on GitHub to reply to yet.
			name:    "reply to a pending top-level comment has no anchor",
			comment: localComment(2, ptr(1)),
			pending: []database.LocalComment{localComment(1, nil)},
			want:    0,
		},
		{
			name:    "cycle does not hang",
			comment: localComment(1, ptr(2)),
			pending: []database.LocalComment{localComment(2, ptr(1))},
			want:    0,
		},
		{
			name:    "self-reference does not hang",
			comment: localComment(1, ptr(1)),
			pending: []database.LocalComment{localComment(1, ptr(1))},
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pending := map[int64]database.LocalComment{tt.comment.ID: tt.comment}
			for _, c := range tt.pending {
				pending[c.ID] = c
			}
			got, ok := resolveReplyAnchor(tt.comment, pending)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("resolveReplyAnchor() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
