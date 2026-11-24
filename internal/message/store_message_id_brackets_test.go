package message

import (
	"path/filepath"
	"testing"

	"github.com/absonsnet/daak/internal/database"
)

// newBracketFormsTestStore opens a migrated temp DB with one account and
// inbox/trash/spam/sent folders, for Message-ID bracket-form tests.
func newBracketFormsTestStore(t *testing.T) *Store {
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

	if _, err := db.Exec(
		`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username) VALUES (?, ?, ?, ?, ?, ?)`,
		"acct-1", "Test", "test@example.com", "imap.example.com", "smtp.example.com", "test@example.com",
	); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	for _, f := range [][3]string{
		{"inbox-1", "INBOX", "inbox"},
		{"trash-1", "Trash", "trash"},
		{"spam-1", "Spam", "spam"},
		{"sent-1", "Sent", "sent"},
	} {
		if _, err := db.Exec(
			`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES (?, ?, ?, ?, ?)`,
			f[0], "acct-1", f[1], f[1], f[2],
		); err != nil {
			t.Fatalf("seed folder %s: %v", f[0], err)
		}
	}
	return NewStore(db)
}

// TestMessageIDBracketForms guards finding 20: the same message can be stored
// under either Message-ID form — bracket-less from the IMAP envelope and
// header-recovery paths (go-message strips the brackets), bracketed from
// locally created rows (sent mail carries `<uuid@daak>`) and legacy rows.
// All three exact-equality lookups must match across forms, in both
// directions, or the Gmail Trash/Spam guard, bulk actions on duplicate
// copies, and the Undo destination lookup silently miss.
func TestMessageIDBracketForms(t *testing.T) {
	s := newBracketFormsTestStore(t)

	// Same message, two copies in different forms: the inbox row as stored
	// bracketed, the trash row as the recovery path stored it (no brackets).
	seedCopy(t, s, "prim", "inbox-1", "<prim@example.com>", 6)
	seedCopy(t, s, "prim-trash", "trash-1", "prim@example.com", 7)
	// A recovered row only present in trash.
	seedCopy(t, s, "rec", "trash-1", "rec@example.com", 5)
	// Same message twice in one folder, once per form.
	seedCopy(t, s, "dup-a", "inbox-1", "<dup@example.com>", 10)
	seedCopy(t, s, "dup-b", "inbox-1", "dup@example.com", 11)
	// Same ID in another folder must never leak into folder-scoped lookups.
	seedCopy(t, s, "dup-sent", "sent-1", "<dup@example.com>", 12)

	// ExistsInFolder: bracketed query must find the bracket-less trash row
	// (the Gmail guard: the inbox row's ID is bracketed, the trash copy is
	// not — an exact match would delete the message instead of preserving it).
	if ok, err := s.ExistsInFolder("<prim@example.com>", "trash", "acct-1"); err != nil || !ok {
		t.Fatalf("ExistsInFolder(<prim@example.com>, trash) = %v, %v; want true", ok, err)
	}
	if ok, err := s.ExistsInFolder("prim@example.com", "trash", "acct-1"); err != nil || !ok {
		t.Fatalf("ExistsInFolder(prim@example.com, trash) = %v, %v; want true", ok, err)
	}
	if ok, err := s.ExistsInFolder("<rec@example.com>", "trash", "acct-1"); err != nil || !ok {
		t.Fatalf("ExistsInFolder(<rec@example.com>, trash) = %v, %v; want true", ok, err)
	}
	if ok, err := s.ExistsInFolder("rec@example.com", "spam", "acct-1"); err != nil || ok {
		t.Fatalf("ExistsInFolder(rec@example.com, spam) = %v, %v; want false", ok, err)
	}
	if ok, err := s.ExistsInFolder("prim@example.com", "inbox", "acct-1"); err != nil || !ok {
		t.Fatalf("ExistsInFolder(prim@example.com, inbox) = %v, %v; want true", ok, err)
	}

	// ExpandToFolderCopies: the two same-folder copies must expand to each
	// other despite the bracket difference; the sent copy is untouched.
	got, err := s.ExpandToFolderCopies([]string{"dup-a"})
	if err != nil {
		t.Fatalf("ExpandToFolderCopies: %v", err)
	}
	want := map[string]bool{"dup-a": true, "dup-b": true}
	if len(got) != len(want) {
		t.Fatalf("ExpandToFolderCopies([dup-a]) = %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected id %q in %v", id, got)
		}
	}
	got, err = s.ExpandToFolderCopies([]string{"dup-b"})
	if err != nil {
		t.Fatalf("ExpandToFolderCopies(dup-b): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ExpandToFolderCopies([dup-b]) = %v, want both inbox copies", got)
	}

	// GetIDsByMessageIDs: either form of the query must find both rows
	// (the Undo destination lookup: the caller passes the ID as stored on
	// the moved row, the destination row may carry the other form).
	for _, q := range []string{"<dup@example.com>", "dup@example.com"} {
		ids, err := s.GetIDsByMessageIDs("acct-1", "inbox-1", []string{q})
		if err != nil {
			t.Fatalf("GetIDsByMessageIDs(%q): %v", q, err)
		}
		if len(ids) != 2 {
			t.Fatalf("GetIDsByMessageIDs(%q) = %v, want both inbox copies", q, ids)
		}
	}
	ids, err := s.GetIDsByMessageIDs("acct-1", "sent-1", []string{"dup@example.com"})
	if err != nil {
		t.Fatalf("GetIDsByMessageIDs(sent): %v", err)
	}
	if len(ids) != 1 || ids[0] != "dup-sent" {
		t.Fatalf("GetIDsByMessageIDs(sent) = %v, want [dup-sent]", ids)
	}

	// HasCopiesInOtherFolders (same bracket defect, Gmail permanent-delete
	// guard): bracketed query must see the bracket-less trash copy.
	if ok, err := s.HasCopiesInOtherFolders("<prim@example.com>", "inbox-1", "acct-1"); err != nil || !ok {
		t.Fatalf("HasCopiesInOtherFolders(<prim@example.com>, inbox-1) = %v, %v; want true", ok, err)
	}
	if ok, err := s.HasCopiesInOtherFolders("rec@example.com", "trash-1", "acct-1"); err != nil || ok {
		t.Fatalf("HasCopiesInOtherFolders(rec@example.com, trash-1) = %v, %v; want false", ok, err)
	}
}

// TestExpandToFolderCopiesBareBrackets ensures a row whose Message-ID is just
// "<>" does not expand onto empty-Message-ID rows: the non-empty check runs
// on the bracket-stripped form.
func TestExpandToFolderCopiesBareBrackets(t *testing.T) {
	s := newBracketFormsTestStore(t)
	seedCopy(t, s, "weird", "inbox-1", "<>", 13)
	seedCopy(t, s, "blank", "inbox-1", "", 14)

	got, err := s.ExpandToFolderCopies([]string{"weird"})
	if err != nil {
		t.Fatalf("ExpandToFolderCopies: %v", err)
	}
	if len(got) != 1 || got[0] != "weird" {
		t.Fatalf("ExpandToFolderCopies([weird]) = %v, want [weird]", got)
	}
}
