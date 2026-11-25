package message

import (
	"testing"
	"time"
)

// Re-audit finding R10: an attachment-only fetch whose text parts failed
// to read is not usable content, while a genuinely attachment-only fetch
// still is. BodyUpdate.HasUsableContent is the shared "resolved"
// definition, so these cases pin it directly; the store tests below pin
// that the flag itself now reaches the frontend through Get/GetByUID
// (BodyFailed is json:"bodyFailed", not json:"-").

func TestBodyUpdateHasUsableContent_TextPartsFailed(t *testing.T) {
	cases := []struct {
		name string
		u    BodyUpdate
		want bool
	}{
		{
			name: "genuinely attachment-only: usable",
			u:    BodyUpdate{HasAttachments: true},
			want: true,
		},
		{
			name: "attachments plus failed text part, no text: NOT usable",
			u:    BodyUpdate{HasAttachments: true, TextPartsFailed: true},
			want: false,
		},
		{
			name: "failed text part but text recovered: usable",
			u:    BodyUpdate{BodyText: "hello", HasAttachments: true, TextPartsFailed: true},
			want: true,
		},
		{
			name: "failed text part but HTML recovered: usable",
			u:    BodyUpdate{BodyHTML: "<p>hi</p>", TextPartsFailed: true},
			want: true,
		},
		{
			name: "failed text part, encrypted payload: usable (decryptable on view)",
			u:    BodyUpdate{HasAttachments: true, TextPartsFailed: true, SMIMEEncrypted: true},
			want: true,
		},
		{
			name: "empty, no failure: not usable",
			u:    BodyUpdate{},
			want: false,
		},
		{
			name: "body text only: usable",
			u:    BodyUpdate{BodyText: "hello"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.HasUsableContent(); got != tc.want {
				t.Errorf("HasUsableContent() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpdateBodiesBatch_TextPartsFailedDoesNotClearFlag: a re-fetch that
// fails the text parts again must not clear a body_failed flag the batch
// pass just set — the mirror of the existing "empty update leaves the flag
// alone" rule, for the attachment-only shape.
func TestUpdateBodiesBatch_TextPartsFailedDoesNotClearFlag(t *testing.T) {
	s, db, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	m := &Message{ID: "att-failed", AccountID: accountID, FolderID: folderID, UID: 1, Date: now, BodyFetched: true}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.MarkBodyFailed([]string{"att-failed"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}
	if err := s.UpdateBodiesBatch([]BodyUpdate{
		{MessageID: "att-failed", HasAttachments: true, TextPartsFailed: true},
	}); err != nil {
		t.Fatalf("UpdateBodiesBatch: %v", err)
	}
	if got := bodyFailedFlag(t, db, "att-failed"); got != 1 {
		t.Errorf("body_failed = %d, want 1 (a still-failing re-fetch must not clear the flag)", got)
	}
}

// TestGetExposesBodyFailed pins the frontend half of R10: the flag is
// read back through the same Get/GetByUID paths the viewer uses, so a
// flagged message arrives with BodyFailed=true and the viewer can offer
// a retry. (Before R10 the model tag was json:"-" and these queries did
// not select the column at all.)
func TestGetExposesBodyFailed(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	m := &Message{ID: "failed-1", AccountID: accountID, FolderID: folderID, UID: 1, Date: now, BodyFetched: true}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, err := s.Get("failed-1"); err != nil || got == nil || got.BodyFailed {
		t.Fatalf("Get before flag: BodyFailed=%v err=%v, want false", got.BodyFailed, err)
	}
	if err := s.MarkBodyFailed([]string{"failed-1"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}
	got, err := s.Get("failed-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || !got.BodyFailed {
		t.Fatalf("Get after flag: BodyFailed=%v, want true", got.BodyFailed)
	}
	byUID, err := s.GetByUID(folderID, 1)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if byUID == nil || !byUID.BodyFailed {
		t.Fatalf("GetByUID after flag: BodyFailed=%v, want true", byUID.BodyFailed)
	}
}
