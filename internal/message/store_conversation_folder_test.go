package message

import (
	"testing"
	"time"
)

// TestListConversationsByFolderUsesOwnFoldersCopy (fourth-audit finding
// 49): a thread with copies in two folders must show the code chip /
// category from THIS folder's copy. The kind/value/rule/category
// subqueries used to pick the latest message of the thread in the
// whole account, so a newer copy in another folder leaked its chip
// and tab into this folder's row.
func TestListConversationsByFolderUsesOwnFoldersCopy(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	const otherFolderID = "folder-2" // seeded by newBodyFailedTestStore as Archive

	// Same thread in both folders. The other folder's copy is newer,
	// so an unscoped subquery would pick its chip/category.
	inFolder := &Message{
		ID: "conv-a", AccountID: accountID, FolderID: folderID,
		UID: 1, ThreadID: "thread-1", Subject: "Your code is 111111",
		FromEmail: "a@example.com", Date: now.Add(-1 * time.Hour),
		ExtractKind: "code", ExtractValue: "111111", ExtractRule: "otp",
		Category: "automated", SubCategory: "Transactions",
	}
	otherFolder := &Message{
		ID: "conv-b", AccountID: accountID, FolderID: otherFolderID,
		UID: 1, ThreadID: "thread-1", Subject: "Flight changed",
		FromEmail: "b@example.com", Date: now,
		ExtractKind: "flight", ExtractValue: "BA2490", ExtractRule: "flight",
		Category: "personal",
	}
	for _, m := range []*Message{inFolder, otherFolder} {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}

	convs, err := s.ListConversationsByFolder(folderID, 0, 10, "newest", "", "", "")
	if err != nil {
		t.Fatalf("ListConversationsByFolder: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("ListConversationsByFolder = %d conversations, want 1", len(convs))
	}
	c := convs[0]
	if c.ExtractKind != "code" || c.ExtractValue != "111111" || c.ExtractRule != "otp" {
		t.Errorf("extract = %q/%q/%q, want code/111111/otp (this folder's copy)",
			c.ExtractKind, c.ExtractValue, c.ExtractRule)
	}
	if c.ExtractMessageID != "conv-a" {
		t.Errorf("ExtractMessageID = %q, want conv-a", c.ExtractMessageID)
	}
	if c.Category != "automated" || c.SubCategory != "Transactions" {
		t.Errorf("category = %q/%q, want automated/Transactions (this folder's copy)",
			c.Category, c.SubCategory)
	}
}
