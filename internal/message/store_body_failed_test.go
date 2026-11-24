package message

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/database"
)

// newBodyFailedTestStore opens a migrated temp DB, seeds one (account, folder)
// row, and returns a message Store ready to attach test messages to. Mirrors
// the test-scaffolding patterns in internal/database/database_test.go.
func newBodyFailedTestStore(t *testing.T) (*Store, *database.DB, string, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const accountID = "acct-1"
	const folderID = "folder-1"

	if _, err := db.Exec(
		`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		accountID, "Test", "test@example.com", "imap.example.com", "smtp.example.com", "test@example.com",
	); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		folderID, accountID, "INBOX", "INBOX", "inbox",
	); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	// Second folder, so ClearBodiesForFolder tests can prove the flag is
	// cleared only in the folder being force re-synced.
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		"folder-2", accountID, "Archive", "Archive", "archive",
	); err != nil {
		t.Fatalf("seed folder-2: %v", err)
	}

	return NewStore(db), db, accountID, folderID
}

// bodyFailedFlag reads the raw body_failed column for one message.
func bodyFailedFlag(t *testing.T, db *database.DB, id string) int {
	t.Helper()
	var flag int
	if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, id).Scan(&flag); err != nil {
		t.Fatalf("query body_failed for %s: %v", id, err)
	}
	return flag
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestMarkBodyFailed_ExcludesFromQueue confirms the persistent flag actually
// drops a message from GetMessagesWithoutBody on subsequent calls — the whole
// point of v39. Without the flag, an unparseable message costs an IMAP FETCH
// per sync cycle forever.
func TestMarkBodyFailed_ExcludesFromQueue(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	// Fetched-but-empty, non-encrypted → matches the self-healing branch of
	// GetMessagesWithoutBody, so it should be queued for re-fetch until we
	// flag it.
	stuck := &Message{
		ID:          "stuck",
		AccountID:   accountID,
		FolderID:    folderID,
		UID:         1,
		Date:        now,
		BodyFetched: true,
	}
	if err := s.Create(stuck); err != nil {
		t.Fatalf("Create stuck message: %v", err)
	}

	// Sanity: queued before flagging.
	ids, err := s.GetMessagesWithoutBody(folderID, 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody (pre-mark): %v", err)
	}
	if !containsID(ids, "stuck") {
		t.Fatalf("pre-mark: expected stuck message in queue, got %v", ids)
	}

	// Flag it.
	if err := s.MarkBodyFailed([]string{"stuck"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}

	// After flagging, the message must no longer be queued — that's the only
	// behavior this fix has to guarantee.
	ids, err = s.GetMessagesWithoutBody(folderID, 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody (post-mark): %v", err)
	}
	if containsID(ids, "stuck") {
		t.Errorf("post-mark: expected stuck message out of queue, got %v", ids)
	}

	// Idempotent: re-flagging an already-flagged row must not error.
	if err := s.MarkBodyFailed([]string{"stuck"}); err != nil {
		t.Fatalf("MarkBodyFailed (re-flag): %v", err)
	}

	// Empty input must be a no-op.
	if err := s.MarkBodyFailed(nil); err != nil {
		t.Errorf("MarkBodyFailed(nil): %v", err)
	}
	if err := s.MarkBodyFailed([]string{}); err != nil {
		t.Errorf("MarkBodyFailed([]): %v", err)
	}
}

// TestClearBodiesForFolder_ClearsBodyFailed is the Force Resync retry path
// for audit finding 11: a message flagged body_failed must come back into
// the body-fetch queue after its folder's bodies are cleared, so the next
// body pass re-fetches and re-parses it (including reaction detection).
// The flag must be cleared only in the folder being re-synced.
func TestClearBodiesForFolder_ClearsBodyFailed(t *testing.T) {
	s, db, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	stuck := &Message{
		ID:          "stuck",
		AccountID:   accountID,
		FolderID:    folderID,
		UID:         1,
		Date:        now,
		BodyFetched: true,
	}
	if err := s.Create(stuck); err != nil {
		t.Fatalf("Create stuck message: %v", err)
	}
	other := &Message{
		ID:          "other-folder",
		AccountID:   accountID,
		FolderID:    "folder-2",
		UID:         1,
		Date:        now,
		BodyFetched: true,
	}
	if err := s.Create(other); err != nil {
		t.Fatalf("Create other-folder message: %v", err)
	}

	if err := s.MarkBodyFailed([]string{"stuck", "other-folder"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}

	// Force Resync step 1 for folder-1 only.
	if _, err := s.ClearBodiesForFolder(folderID); err != nil {
		t.Fatalf("ClearBodiesForFolder: %v", err)
	}

	if got := bodyFailedFlag(t, db, "stuck"); got != 0 {
		t.Errorf("body_failed for cleared folder = %d, want 0 (Force Resync must reset it)", got)
	}
	if got := bodyFailedFlag(t, db, "other-folder"); got != 1 {
		t.Errorf("body_failed for untouched folder = %d, want 1 (clear is folder-scoped)", got)
	}

	// And the message is a body-fetch candidate again.
	ids, err := s.GetMessagesWithoutBody(folderID, 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody (post-clear): %v", err)
	}
	if !containsID(ids, "stuck") {
		t.Errorf("post-clear: expected stuck message back in queue, got %v", ids)
	}
	sized, err := s.GetMessagesWithoutBodyAndSize("folder-2", 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBodyAndSize (folder-2): %v", err)
	}
	for _, m := range sized {
		if m.ID == "other-folder" {
			t.Errorf("untouched folder: other-folder message still queued, want it excluded by body_failed=1")
		}
	}
}

// TestAttachmentOnlyMessage_NotRequeued: a message whose fetch produced
// attachments but no body text is complete, not failed — it must not be
// re-queued for body fetch every sync (the loop body_failed was created
// to stop), and it must not need the flag to stay out of the queue.
func TestAttachmentOnlyMessage_NotRequeued(t *testing.T) {
	s, db, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	attOnly := &Message{
		ID:             "att-only",
		AccountID:      accountID,
		FolderID:       folderID,
		UID:            1,
		Date:           now,
		BodyFetched:    true,
		HasAttachments: true,
	}
	if err := s.Create(attOnly); err != nil {
		t.Fatalf("Create attachment-only message: %v", err)
	}

	// No body_failed flag involved: the row must stay out of the queue on
	// the strength of has_attachments alone.
	if got := bodyFailedFlag(t, db, "att-only"); got != 0 {
		t.Fatalf("body_failed for attachment-only message = %d, want 0", got)
	}
	ids, err := s.GetMessagesWithoutBody(folderID, 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody: %v", err)
	}
	if containsID(ids, "att-only") {
		t.Errorf("attachment-only message queued for re-fetch, want it excluded (has_attachments=1 means fetched)")
	}
	sized, err := s.GetMessagesWithoutBodyAndSize(folderID, 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBodyAndSize: %v", err)
	}
	for _, m := range sized {
		if m.ID == "att-only" {
			t.Errorf("attachment-only message queued by GetMessagesWithoutBodyAndSize, want it excluded")
		}
	}
	count, err := s.CountMessagesWithoutBody(folderID, time.Time{})
	if err != nil {
		t.Fatalf("CountMessagesWithoutBody: %v", err)
	}
	if count != 0 {
		t.Errorf("CountMessagesWithoutBody = %d, want 0 (only the attachment-only message exists)", count)
	}
}

// TestUpdateBodiesBatch_ClearsBodyFailedWhenResolved: a body that arrives
// through any path heals a stale body_failed flag — but only when the
// update actually carries usable content. An empty update must not clear
// a flag the batch pass just set (markUnresolvedAsFailed runs before
// UpdateBodiesBatch in the same iteration).
func TestUpdateBodiesBatch_ClearsBodyFailedWhenResolved(t *testing.T) {
	s, db, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	for _, id := range []string{"with-body", "empty", "att-only"} {
		m := &Message{
			ID:          id,
			AccountID:   accountID,
			FolderID:    folderID,
			UID:         1,
			Date:        now,
			BodyFetched: true,
		}
		// UIDs must be unique per folder.
		switch id {
		case "with-body":
			m.UID = 1
		case "empty":
			m.UID = 2
		case "att-only":
			m.UID = 3
		}
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	if err := s.MarkBodyFailed([]string{"with-body", "empty", "att-only"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}

	err := s.UpdateBodiesBatch([]BodyUpdate{
		{MessageID: "with-body", BodyText: "hello"},
		{MessageID: "empty"},
		{MessageID: "att-only", HasAttachments: true},
	})
	if err != nil {
		t.Fatalf("UpdateBodiesBatch: %v", err)
	}

	if got := bodyFailedFlag(t, db, "with-body"); got != 0 {
		t.Errorf("body_failed after resolved update = %d, want 0", got)
	}
	if got := bodyFailedFlag(t, db, "empty"); got != 1 {
		t.Errorf("body_failed after empty update = %d, want 1 (empty persist must not clear the flag)", got)
	}
	if got := bodyFailedFlag(t, db, "att-only"); got != 0 {
		t.Errorf("body_failed after attachment-only update = %d, want 0 (attachments are usable content)", got)
	}
}

// TestUpdateBody_ClearsBodyFailedWhenResolved is the single-update
// (on-demand fetch) mirror of TestUpdateBodiesBatch_ClearsBodyFailedWhenResolved:
// a body_failed message whose body arrives on open — the body_fetched=0
// subclass, where the app-level guard lets the on-demand fetch run —
// must not keep a stale flag afterwards.
func TestUpdateBody_ClearsBodyFailedWhenResolved(t *testing.T) {
	s, db, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	m := &Message{
		ID:          "ondemand",
		AccountID:   accountID,
		FolderID:    folderID,
		UID:         1,
		Date:        now,
		BodyFetched: false,
	}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create ondemand message: %v", err)
	}
	if err := s.MarkBodyFailed([]string{"ondemand"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}

	if err := s.UpdateBody(BodyUpdate{MessageID: "ondemand", BodyText: "fetched on open"}); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}
	if got := bodyFailedFlag(t, db, "ondemand"); got != 0 {
		t.Errorf("body_failed after on-demand body persist = %d, want 0", got)
	}
}
