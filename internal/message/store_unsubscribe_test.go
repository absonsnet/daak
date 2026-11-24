package message

import (
	"testing"
	"time"
)

// TestUnsubscribeStore covers the Phase C store surface: recording
// actions (only completed methods suppress the button), sender rules,
// the Subscriptions aggregation, and listing a sender's mail in a folder.
func TestUnsubscribeStore(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	seed := func(id string, uid uint32, fromEmail, lu string, post bool, date time.Time) {
		t.Helper()
		m := &Message{
			ID: id, AccountID: accountID, FolderID: folderID,
			UID: uid, Subject: "Newsletter", FromEmail: fromEmail,
			Date: date, ListUnsubscribe: lu, ListUnsubscribePost: post,
			Category: "automated",
		}
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	seed("n1", 1, "news@example.com", "<https://example.com/unsub>", true, now.Add(-1*time.Hour))
	seed("n2", 2, "NEWS@example.com", "<https://example.com/unsub>", true, now.Add(-2*time.Hour))
	seed("n3", 3, "news@example.com", "<https://example.com/unsub>", true, now.Add(-40*24*time.Hour))
	seed("n4", 4, "deals@shop.example", "<mailto:unsub@shop.example>", false, now.Add(-3*time.Hour))
	seed("n5", 5, "friend@example.com", "", false, now.Add(-4*time.Hour))

	// Recording: case-insensitive sender match, completed methods only.
	if has, err := s.HasUnsubscribed("news@example.com", accountID); err != nil || has {
		t.Fatalf("HasUnsubscribed before record = %v, err = %v, want false", has, err)
	}
	if err := s.RecordUnsubscribeAction("NEWS@Example.com", accountID, "browser"); err != nil {
		t.Fatalf("RecordUnsubscribeAction(browser): %v", err)
	}
	// A browser-only action does NOT suppress the button.
	if has, err := s.HasUnsubscribed("news@example.com", accountID); err != nil || has {
		t.Fatalf("HasUnsubscribed after browser = %v, err = %v, want false", has, err)
	}
	if method, ok, err := s.UnsubscribeActionMethod("news@example.com", accountID); err != nil || !ok || method != "browser" {
		t.Fatalf("UnsubscribeActionMethod = %q, %v, %v, want browser/true/nil", method, ok, err)
	}
	// Re-recording with a completed method supersedes it.
	if err := s.RecordUnsubscribeAction("news@example.com", accountID, "one_click"); err != nil {
		t.Fatalf("RecordUnsubscribeAction(one_click): %v", err)
	}
	if has, err := s.HasUnsubscribed("news@example.com", accountID); err != nil || !has {
		t.Fatalf("HasUnsubscribed after one_click = %v, err = %v, want true", has, err)
	}

	// Sender rules: set / has / list / delete, case-insensitive.
	if err := s.SetSenderRule("Deals@Shop.example", accountID, "auto_trash"); err != nil {
		t.Fatalf("SetSenderRule: %v", err)
	}
	if has, err := s.HasSenderRule("deals@shop.example", accountID, "auto_trash"); err != nil || !has {
		t.Fatalf("HasSenderRule = %v, err = %v, want true", has, err)
	}
	senders, err := s.ListSenderRuleSenders(accountID, "auto_trash")
	if err != nil || len(senders) != 1 || senders[0] != "deals@shop.example" {
		t.Fatalf("ListSenderRuleSenders = %v, err = %v", senders, err)
	}
	if err := s.DeleteSenderRule("deals@shop.example", accountID, "auto_trash"); err != nil {
		t.Fatalf("DeleteSenderRule: %v", err)
	}
	if has, err := s.HasSenderRule("deals@shop.example", accountID, "auto_trash"); err != nil || has {
		t.Fatalf("HasSenderRule after delete = %v, err = %v, want false", has, err)
	}

	// Subscriptions aggregation: only senders with headers, most first,
	// 30-day count excludes the 40-day-old message, one-click flag kept.
	subs, err := s.ListSubscriptionSenders()
	if err != nil {
		t.Fatalf("ListSubscriptionSenders: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("ListSubscriptionSenders = %d senders, want 2", len(subs))
	}
	top := subs[0]
	if top.SenderEmail != "news@example.com" || top.CountAll != 3 || top.Count30Days != 2 {
		t.Fatalf("top sender = %+v, want news@example.com 3/2", top)
	}
	if !top.OneClick {
		t.Fatalf("top.OneClick = false, want true")
	}
	if top.ListUnsubscribe != "<https://example.com/unsub>" {
		t.Fatalf("top.ListUnsubscribe = %q", top.ListUnsubscribe)
	}
	if top.SenderName != "" {
		t.Fatalf("top.SenderName = %q, want empty (no from_name seeded)", top.SenderName)
	}
	second := subs[1]
	if second.SenderEmail != "deals@shop.example" || second.CountAll != 1 || second.OneClick {
		t.Fatalf("second sender = %+v, want deals@shop.example 1/no-one-click", second)
	}

	// Sender's mail in the folder: case-insensitive, oldest first.
	ids, err := s.ListMessageIDsFromSenderInFolder(accountID, folderID, "NEWS@example.com")
	if err != nil {
		t.Fatalf("ListMessageIDsFromSenderInFolder: %v", err)
	}
	if len(ids) != 3 || ids[0] != "n3" || ids[1] != "n2" || ids[2] != "n1" {
		t.Fatalf("sender ids = %v, want [n3 n2 n1]", ids)
	}
	ids, err = s.ListMessageIDsFromSenderInFolder(accountID, folderID, "nobody@example.com")
	if err != nil || len(ids) != 0 {
		t.Fatalf("unknown sender ids = %v, err = %v, want empty", ids, err)
	}
}

// TestListUnenrichedPreservesListUnsubscribe checks the backfill input
// carries the stored List-Unsubscribe columns, so the backfill can never
// wipe a header-time value it cannot recover.
func TestListUnenrichedPreservesListUnsubscribe(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	m := &Message{
		ID: "u1", AccountID: accountID, FolderID: folderID,
		UID: 1, Subject: "Newsletter", FromEmail: "news@example.com",
		Date: time.Now().UTC(), ListUnsubscribe: "<https://example.com/unsub>",
		ListUnsubscribePost: true,
	}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rows, err := s.ListUnenriched(10)
	if err != nil {
		t.Fatalf("ListUnenriched: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListUnenriched = %d, want 1", len(rows))
	}
	if rows[0].ListUnsubscribe != "<https://example.com/unsub>" || !rows[0].ListUnsubscribePost {
		t.Fatalf("unenriched row = %+v, want LU preserved", rows[0])
	}
}
