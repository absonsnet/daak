package message

import (
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/enrich"
)

// TestCategory_FilterAndCounts covers the Phase E store surface: the
// category tab filter on the conversation list/count (by latest message's
// category, never-classified rows counting as Personal), the per-tab
// counts, and the sender-override write path (set/get/delete + re-file).
func TestCategory_FilterAndCounts(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	auto := &Message{
		ID: "auto-1", AccountID: accountID, FolderID: folderID,
		UID: 1, Subject: "Your NBP OTP is 482619", FromEmail: "alerts@nbp.com.pk",
		Date: now.Add(-1 * time.Hour), Snippet: "Your OTP is 482619",
	}
	personal := &Message{
		ID: "pers-1", AccountID: accountID, FolderID: folderID,
		UID: 2, Subject: "Dinner?", FromEmail: "sara@example.com",
		Date: now.Add(-2 * time.Hour),
	}
	unclassified := &Message{
		ID: "unc-1", AccountID: accountID, FolderID: folderID,
		UID: 3, Subject: "Hello", FromEmail: "tom@example.com",
		Date: now.Add(-3 * time.Hour),
	}
	for _, m := range []*Message{auto, personal, unclassified} {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}
	// Classify the first two; the third stays NULL (never classified).
	// NOTE: each fixture gets its own body head — the personal one must
	// not contain OTP text, or classifySubCategory files it automated.
	bodyHeads := map[string]string{
		"auto-1": "Your one-time password is 482619.",
		"pers-1": "Shall we meet at seven?",
	}
	for _, m := range []*Message{auto, personal} {
		res := enrich.Enrich(enrich.Input{
			FromEmail: m.FromEmail, Subject: m.Subject, Snippet: m.Snippet,
			BodyHead: bodyHeads[m.ID],
		})
		if err := s.UpdateEnrichment(m.ID, res); err != nil {
			t.Fatalf("UpdateEnrichment %s: %v", m.ID, err)
		}
	}
	got, err := s.Get(auto.ID)
	if err != nil || got.Category != enrich.CategoryAutomated || got.SubCategory != "Transactions" {
		t.Fatalf("automated fixture: category=%q sub=%q err=%v", got.Category, got.SubCategory, err)
	}

	list := func(category, sub string) []*Conversation {
		t.Helper()
		convs, err := s.ListConversationsByFolder(folderID, 0, 10, "newest", "", category, sub)
		if err != nil {
			t.Fatalf("ListConversationsByFolder(%q,%q): %v", category, sub, err)
		}
		return convs
	}
	count := func(category, sub string) int {
		t.Helper()
		n, err := s.CountConversationsByFolder(folderID, "", category, sub)
		if err != nil {
			t.Fatalf("CountConversationsByFolder(%q,%q): %v", category, sub, err)
		}
		return n
	}

	// All: every conversation, each carrying its latest category.
	if convs := list("", ""); len(convs) != 3 {
		t.Fatalf("All tab: got %d conversations, want 3", len(convs))
	} else {
		byID := map[string]*Conversation{}
		for _, c := range convs {
			byID[c.MessageIDs[0]] = c
		}
		if byID["auto-1"].Category != "automated" || byID["auto-1"].SubCategory != "Transactions" {
			t.Errorf("All tab: automated row category=%q sub=%q", byID["auto-1"].Category, byID["auto-1"].SubCategory)
		}
		if byID["pers-1"].Category != "personal" {
			t.Errorf("All tab: personal row category=%q", byID["pers-1"].Category)
		}
		if byID["unc-1"].Category != "" {
			t.Errorf("All tab: unclassified row category=%q, want empty", byID["unc-1"].Category)
		}
	}

	// Automated tab: only the bank mail; the Transactions chip narrows it.
	if convs := list("automated", ""); len(convs) != 1 || convs[0].MessageIDs[0] != "auto-1" {
		t.Fatalf("Automated tab: got %d conversations, want 1 (auto-1)", len(convs))
	}
	if convs := list("automated", "Transactions"); len(convs) != 1 {
		t.Fatalf("Transactions chip: got %d, want 1", len(convs))
	}
	if convs := list("automated", "Travel"); len(convs) != 0 {
		t.Fatalf("Travel chip: got %d, want 0", len(convs))
	}
	if n := count("automated", ""); n != 1 {
		t.Fatalf("Automated count = %d, want 1", n)
	}

	// Personal tab: the person plus the never-classified row (NULL counts
	// as Personal so un-backfilled mail never vanishes from this tab).
	if convs := list("personal", ""); len(convs) != 2 {
		t.Fatalf("Personal tab: got %d conversations, want 2", len(convs))
	}
	if n := count("personal", ""); n != 2 {
		t.Fatalf("Personal count = %d, want 2", n)
	}

	// Counts: 2 personal (incl. unclassified) + 1 automated / Transactions.
	counts, err := s.GetCategoryCounts(folderID, false)
	if err != nil {
		t.Fatalf("GetCategoryCounts: %v", err)
	}
	if counts.Personal != 2 || counts.Automated != 1 || counts.Total != 3 {
		t.Fatalf("counts = %+v, want personal=2 automated=1 total=3", counts)
	}
	if counts.SubCategories["Transactions"] != 1 {
		t.Fatalf("sub counts = %v, want Transactions=1", counts.SubCategories)
	}

	// Unknown tab value: nothing, never the whole folder.
	if convs := list("bogus", ""); len(convs) != 0 {
		t.Fatalf("bogus tab: got %d, want 0", len(convs))
	}
}

// TestSenderCategoryOverride_WritePath covers set/get/list/delete plus the
// re-file: setting Personal on the automated sender moves its stored mail
// to the Personal tab and clears the sub-category and extraction.
func TestSenderCategoryOverride_WritePath(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	m := &Message{
		ID: "auto-1", AccountID: accountID, FolderID: folderID,
		UID: 1, Subject: "Your NBP OTP is 482619", FromEmail: "Alerts@nbp.com.pk",
		Date: now.Add(-1 * time.Hour), Snippet: "Your OTP is 482619",
	}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	res := enrich.Enrich(enrich.Input{
		FromEmail: m.FromEmail, Subject: m.Subject, Snippet: m.Snippet,
		BodyHead: "Your one-time password is 482619.",
	})
	if err := s.UpdateEnrichment(m.ID, res); err != nil {
		t.Fatalf("UpdateEnrichment: %v", err)
	}

	if err := s.SetSenderCategoryOverride("ALERTS@nbp.com.pk", "personal"); err != nil {
		t.Fatalf("SetSenderCategoryOverride: %v", err)
	}
	if cat, ok, err := s.GetSenderCategoryOverride("alerts@nbp.com.pk"); err != nil || !ok || cat != "personal" {
		t.Fatalf("GetSenderCategoryOverride = %q, %v, %v", cat, ok, err)
	}
	if err := s.SetSenderCategoryOverride("alerts@nbp.com.pk", "bogus"); err == nil {
		t.Fatalf("SetSenderCategoryOverride(bogus) accepted an invalid category")
	}

	n, err := s.ApplySenderCategoryOverride("alerts@nbp.com.pk", "personal")
	if err != nil || n != 1 {
		t.Fatalf("ApplySenderCategoryOverride = %d, %v, want 1", n, err)
	}
	got, err := s.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Category != "personal" || got.SubCategory != "" || got.ExtractKind != "" || got.ExtractValue != "" {
		t.Fatalf("re-filed row = category %q sub %q extract %q/%q, want personal/empty",
			got.Category, got.SubCategory, got.ExtractKind, got.ExtractValue)
	}
	// The row now shows in the Personal tab, not Automated.
	if convs, err := s.ListConversationsByFolder(folderID, 0, 10, "newest", "", "automated", ""); err != nil || len(convs) != 0 {
		t.Fatalf("Automated tab after override: %d conversations, err %v, want 0", len(convs), err)
	}

	overrides, err := s.ListSenderCategoryOverrides()
	if err != nil {
		t.Fatalf("ListSenderCategoryOverrides: %v", err)
	}
	if overrides["alerts@nbp.com.pk"] != "personal" {
		t.Fatalf("ListSenderCategoryOverrides = %v", overrides)
	}
	if err := s.DeleteSenderCategoryOverride("alerts@nbp.com.pk"); err != nil {
		t.Fatalf("DeleteSenderCategoryOverride: %v", err)
	}
	if _, ok, err := s.GetSenderCategoryOverride("alerts@nbp.com.pk"); err != nil || ok {
		t.Fatalf("override still present after delete: ok=%v err=%v", ok, err)
	}
}
