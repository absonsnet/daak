package message

import (
	"testing"
	"time"
)

// TestUnifiedFolder_CombinesAcrossAccounts covers the unified folder views
// (v0.7.1): Sent/Drafts/Spam/Trash merge the same folder type across all
// accounts, Spam combines providers' differently-named folders (Spam vs
// Junk — both classified 'spam' at sync), and Starred is flag-based across
// all folders and accounts. An unknown type is an error, not an empty list.
func TestUnifiedFolder_CombinesAcrossAccounts(t *testing.T) {
	s, _, acct1, inbox1 := newBodyFailedTestStore(t)
	db := s.db
	now := time.Now().UTC()

	// Second account with its own folders. Note the differently-named spam
	// folder ("Junk") — same folder_type 'spam' as acct-1's "Spam".
	const acct2 = "acct-2"
	if _, err := db.Exec(
		`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		acct2, "Test 2", "test2@example.com", "imap.example.com", "smtp.example.com", "test2@example.com",
	); err != nil {
		t.Fatalf("seed account 2: %v", err)
	}
	folders := map[string][3]string{
		// folderID: {accountID, name, type}
		"sent-1":   {acct1, "Sent", "sent"},
		"spam-1":   {acct1, "Spam", "spam"},
		"trash-1":  {acct1, "Trash", "trash"},
		"inbox-2":  {acct2, "INBOX", "inbox"},
		"sent-2":   {acct2, "Sent", "sent"},
		"junk-2":   {acct2, "Junk", "spam"},
		"drafts-2": {acct2, "Drafts", "drafts"},
	}
	for id, f := range folders {
		if _, err := db.Exec(
			`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES (?, ?, ?, ?, ?)`,
			id, f[0], f[1], f[1], f[2],
		); err != nil {
			t.Fatalf("seed folder %s: %v", id, err)
		}
	}
	// Folder unread_count values the badge query reads (cached sums).
	if _, err := db.Exec(`UPDATE folders SET unread_count = 1 WHERE id IN (?, ?)`, inbox1, "sent-2"); err != nil {
		t.Fatalf("seed unread counts: %v", err)
	}

	msgs := []*Message{
		{ID: "m-sent-1", AccountID: acct1, FolderID: "sent-1", UID: 1, Subject: "Sent A", FromEmail: "me@example.com", Date: now.Add(-1 * time.Hour), IsRead: true, IsStarred: true},
		{ID: "m-sent-2", AccountID: acct2, FolderID: "sent-2", UID: 1, Subject: "Sent B", FromEmail: "me2@example.com", Date: now.Add(-2 * time.Hour), IsRead: false},
		{ID: "m-spam-1", AccountID: acct1, FolderID: "spam-1", UID: 2, Subject: "Spam A", FromEmail: "spam@example.com", Date: now.Add(-3 * time.Hour), IsRead: true},
		{ID: "m-junk-2", AccountID: acct2, FolderID: "junk-2", UID: 2, Subject: "Junk B", FromEmail: "junk@example.com", Date: now.Add(-4 * time.Hour), IsRead: true},
		{ID: "m-in-1", AccountID: acct1, FolderID: inbox1, UID: 3, Subject: "Inbox A", FromEmail: "a@example.com", Date: now.Add(-5 * time.Hour), IsRead: false, IsStarred: true},
		{ID: "m-in-2", AccountID: acct2, FolderID: "inbox-2", UID: 3, Subject: "Inbox B", FromEmail: "b@example.com", Date: now.Add(-6 * time.Hour), IsRead: true, IsStarred: true},
		{ID: "m-trash-1", AccountID: acct1, FolderID: "trash-1", UID: 4, Subject: "Trash A", FromEmail: "c@example.com", Date: now.Add(-7 * time.Hour), IsRead: true},
		{ID: "m-draft-2", AccountID: acct2, FolderID: "drafts-2", UID: 4, Subject: "Draft B", FromEmail: "me2@example.com", Date: now.Add(-8 * time.Hour), IsRead: true},
	}
	for _, m := range msgs {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}

	subjects := func(folderType string) map[string]bool {
		t.Helper()
		convs, err := s.ListConversationsUnifiedFolder(folderType, 0, 10, "newest", "", "", "")
		if err != nil {
			t.Fatalf("ListConversationsUnifiedFolder(%q): %v", folderType, err)
		}
		got := map[string]bool{}
		for _, c := range convs {
			got[c.Subject] = true
		}
		return got
	}
	count := func(folderType string) int {
		t.Helper()
		n, err := s.CountConversationsUnifiedFolder(folderType, "", "", "")
		if err != nil {
			t.Fatalf("CountConversationsUnifiedFolder(%q): %v", folderType, err)
		}
		return n
	}
	want := func(folderType string, wantSubjects ...string) {
		t.Helper()
		got := subjects(folderType)
		if len(got) != len(wantSubjects) {
			t.Fatalf("%s: got subjects %v, want %v", folderType, got, wantSubjects)
		}
		for _, w := range wantSubjects {
			if !got[w] {
				t.Fatalf("%s: missing subject %q in %v", folderType, w, got)
			}
		}
		if n := count(folderType); n != len(wantSubjects) {
			t.Fatalf("%s: count %d, want %d", folderType, n, len(wantSubjects))
		}
	}

	// Sent merges both accounts.
	want("sent", "Sent A", "Sent B")
	// Spam merges "Spam" (acct-1) and "Junk" (acct-2).
	want("spam", "Spam A", "Junk B")
	// Trash and Drafts have one conversation each.
	want("trash", "Trash A")
	want("drafts", "Draft B")
	// Inbox still matches the old inbox-only view.
	want("inbox", "Inbox A", "Inbox B")
	// Starred is flag-based across folders and accounts.
	want("starred", "Sent A", "Inbox A", "Inbox B")

	// Unknown type is an error.
	if _, err := s.ListConversationsUnifiedFolder("bogus", 0, 10, "newest", "", "", ""); err == nil {
		t.Fatalf("ListConversationsUnifiedFolder(bogus): expected error, got nil")
	}

	// Badge counts: folder types read the cached folder sums; starred
	// counts unread starred messages directly.
	counts, err := s.GetUnifiedFolderUnreadCounts()
	if err != nil {
		t.Fatalf("GetUnifiedFolderUnreadCounts: %v", err)
	}
	wantCounts := map[string]int{
		"inbox": 1, "starred": 1, "sent": 1, "drafts": 0, "spam": 0, "trash": 0,
	}
	for typ, wantN := range wantCounts {
		if counts[typ] != wantN {
			t.Fatalf("unread %s: got %d, want %d (all: %v)", typ, counts[typ], wantN, counts)
		}
	}
}
