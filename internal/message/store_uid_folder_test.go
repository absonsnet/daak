package message

import (
	"testing"
	"time"
)

// TestExistsByUIDInFolderType (fourth-audit finding 59): the Gmail
// Trash/Spam delete guard falls back to UID + account matching for
// messages with no Message-ID header, so a Trash-hidden message
// without one is still recognised and kept.
func TestExistsByUIDInFolderType(t *testing.T) {
	s, db, accountID, folderID := newBodyFailedTestStore(t)

	const trashFolderID = "folder-3"
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		trashFolderID, accountID, "[Gmail]/Trash", "[Gmail]/Trash", "trash",
	); err != nil {
		t.Fatalf("seed trash folder: %v", err)
	}

	// Same UID in Inbox and Trash, no Message-ID on either row.
	now := time.Now().UTC()
	for _, m := range []*Message{
		{ID: "uid-inbox", AccountID: accountID, FolderID: folderID, UID: 7, Subject: "x", FromEmail: "a@example.com", Date: now},
		{ID: "uid-trash", AccountID: accountID, FolderID: trashFolderID, UID: 7, Subject: "x", FromEmail: "a@example.com", Date: now},
	} {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}

	if got, err := s.ExistsByUIDInFolderType(7, "trash", accountID); err != nil || !got {
		t.Errorf("ExistsByUIDInFolderType(7, trash) = %v, %v; want true, nil", got, err)
	}
	if got, err := s.ExistsByUIDInFolderType(7, "spam", accountID); err != nil || got {
		t.Errorf("ExistsByUIDInFolderType(7, spam) = %v, %v; want false, nil", got, err)
	}
	if got, err := s.ExistsByUIDInFolderType(8, "trash", accountID); err != nil || got {
		t.Errorf("ExistsByUIDInFolderType(8, trash) = %v, %v; want false, nil", got, err)
	}
	if got, err := s.ExistsByUIDInFolderType(7, "trash", "other-account"); err != nil || got {
		t.Errorf("ExistsByUIDInFolderType(7, trash, other-account) = %v, %v; want false, nil", got, err)
	}
}
