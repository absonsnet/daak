package message

import (
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/enrich"
)

// TestEnrichment_RoundTrip covers the Phase A store surface: enrichment
// columns survive Create/Get, the backfill queue lists unenriched rows,
// dismissals persist, and attention candidates surface the right rows.
func TestEnrichment_RoundTrip(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	otp := &Message{
		ID: "otp-1", AccountID: accountID, FolderID: folderID,
		UID: 1, Subject: "Your NBP OTP is 482619", FromEmail: "alerts@nbp.com.pk",
		Date: now.Add(-1 * time.Hour), Snippet: "Your OTP is 482619",
	}
	plain := &Message{
		ID: "plain-1", AccountID: accountID, FolderID: folderID,
		UID: 2, Subject: "Dinner?", FromEmail: "sara@example.com",
		Date: now.Add(-2 * time.Hour),
	}
	for _, m := range []*Message{otp, plain} {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}

	// Both start unenriched (category NULL).
	unenriched, err := s.ListUnenriched(10)
	if err != nil {
		t.Fatalf("ListUnenriched: %v", err)
	}
	if len(unenriched) != 2 {
		t.Fatalf("ListUnenriched = %d, want 2", len(unenriched))
	}
	if n, err := s.CountUnenriched(); err != nil || n != 2 {
		t.Fatalf("CountUnenriched = %d, err = %v, want 2", n, err)
	}

	// Enrich the OTP message; the plain one stays NULL.
	res := enrich.Enrich(enrich.Input{
		FromEmail: otp.FromEmail, Subject: otp.Subject,
		Snippet: otp.Snippet, BodyHead: "Your one-time password is 482619.",
	})
	if res.ExtractValue != "482619" {
		t.Fatalf("fixture enrichment failed: %+v", res)
	}
	if err := s.UpdateEnrichment(otp.ID, res); err != nil {
		t.Fatalf("UpdateEnrichment: %v", err)
	}

	got, err := s.Get(otp.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Category != enrich.CategoryAutomated || got.SubCategory != "Transactions" {
		t.Errorf("Get category = %q/%q, want automated/Transactions", got.Category, got.SubCategory)
	}
	if got.ExtractKind != enrich.ExtractCode || got.ExtractValue != "482619" {
		t.Errorf("Get extract = %q/%q, want code/482619", got.ExtractKind, got.ExtractValue)
	}

	// The row chip data flows through the conversation list.
	convs, err := s.ListConversationsByFolder(folderID, 0, 10, "newest", "", "", "")
	if err != nil {
		t.Fatalf("ListConversationsByFolder: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("ListConversationsByFolder = %d, want 2", len(convs))
	}
	var otpConv *Conversation
	for _, c := range convs {
		if c.Subject == otp.Subject {
			otpConv = c
		}
	}
	if otpConv == nil {
		t.Fatalf("OTP conversation missing from list")
	}
	if otpConv.ExtractKind != enrich.ExtractCode || otpConv.ExtractValue != "482619" {
		t.Errorf("conversation extract = %q/%q, want code/482619", otpConv.ExtractKind, otpConv.ExtractValue)
	}
	if otpConv.ExtractMessageID != otp.ID {
		t.Errorf("conversation extractMessageId = %q, want %q", otpConv.ExtractMessageID, otp.ID)
	}

	// Backfill queue now holds only the plain message.
	unenriched, err = s.ListUnenriched(10)
	if err != nil {
		t.Fatalf("ListUnenriched (2nd): %v", err)
	}
	if len(unenriched) != 1 || unenriched[0].ID != plain.ID {
		t.Fatalf("ListUnenriched (2nd) = %v, want [plain-1]", unenriched)
	}

	// Attention candidates: only the OTP row qualifies.
	cands, err := s.ListAttentionCandidates(nil, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("ListAttentionCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0].Message.ID != otp.ID {
		t.Fatalf("ListAttentionCandidates = %d rows, want [otp-1]", len(cands))
	}
	if cands[0].AccountName != "Test" || cands[0].FolderType != "inbox" {
		t.Errorf("candidate account/folder = %q/%q, want Test/inbox", cands[0].AccountName, cands[0].FolderType)
	}

	// Dismissal persists and is queryable.
	if dismissed, err := s.IsAttentionDismissed(otp.ID); err != nil || dismissed {
		t.Fatalf("IsAttentionDismissed (pre) = %v, err = %v, want false", dismissed, err)
	}
	if err := s.DismissAttention(otp.ID); err != nil {
		t.Fatalf("DismissAttention: %v", err)
	}
	if dismissed, err := s.IsAttentionDismissed(otp.ID); err != nil || !dismissed {
		t.Fatalf("IsAttentionDismissed (post) = %v, err = %v, want true", dismissed, err)
	}
	ids, err := s.GetAttentionDismissalIDs()
	if err != nil || !ids[otp.ID] {
		t.Fatalf("GetAttentionDismissalIDs = %v, err = %v, want otp-1 present", ids, err)
	}

	// Overrides + known-personal-sender lookups.
	if _, ok, err := s.GetSenderCategoryOverride("alerts@nbp.com.pk"); err != nil || ok {
		t.Fatalf("GetSenderCategoryOverride (absent) = %v, err = %v", ok, err)
	}
	if known, err := s.IsKnownPersonalSender("sara@example.com"); err != nil || known {
		t.Fatalf("IsKnownPersonalSender (absent) = %v, err = %v", known, err)
	}
}

func TestMigrationV46_V47_Shape(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	// The migration-46/47 tables exist and accept rows.
	db := s.db
	if _, err := db.Exec(
		`INSERT INTO sender_category_overrides (sender_address, category) VALUES (?, ?)`,
		"alerts@nbp.com.pk", "automated",
	); err != nil {
		t.Fatalf("sender_category_overrides insert: %v", err)
	}
	if cat, ok, err := s.GetSenderCategoryOverride("ALERTS@nbp.com.pk"); err != nil || !ok || cat != "automated" {
		t.Fatalf("GetSenderCategoryOverride (case-insensitive) = %q/%v, err = %v", cat, ok, err)
	}
	if _, err := db.Exec(
		`INSERT INTO unsubscribe_actions (sender_address, account_id, method) VALUES (?, ?, ?)`,
		"news@example.com", accountID, "one-click",
	); err != nil {
		t.Fatalf("unsubscribe_actions insert: %v", err)
	}
	// The migration-47 tables exist.
	for _, tbl := range []string{"attention_dismissals", "message_translations"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing: count = %d, err = %v", tbl, n, err)
		}
	}
	// With a parent message the translation row lands.
	m := &Message{ID: "msg-1", AccountID: accountID, FolderID: folderID, UID: 9, Subject: "Hi", Date: now}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO message_translations (message_id, target_lang, engine, translated_subject, translated_body)
		 VALUES (?, ?, ?, ?, ?)`,
		"msg-1", "ur", "bergamot", "subject", "body",
	); err != nil {
		t.Fatalf("message_translations insert: %v", err)
	}
	// Dismissal table accepts a row via the store method.
	if err := s.DismissAttention("msg-1"); err != nil {
		t.Fatalf("DismissAttention: %v", err)
	}
}

// TestAttentionCandidates_NullExtractFields is a regression test: a
// candidate row whose extract_* columns are NULL (no extraction — e.g. a
// flight-change mail) must scan cleanly instead of failing the whole
// query with "converting NULL to string is unsupported".
func TestAttentionCandidates_NullExtractFields(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	flight := &Message{
		ID: "flight-1", AccountID: accountID, FolderID: folderID,
		UID: 1, Subject: "Your flight EK602 has been cancelled",
		FromEmail: "noreply@emirates.com",
		Date:      now.Add(-3 * time.Hour),
		BodyText:  "Flight EK602 on 20 November 2026 has been cancelled.",
	}
	if err := s.Create(flight); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Enrich it with a result that has NO extraction (category only).
	if err := s.UpdateEnrichment(flight.ID, enrich.Result{
		Category: enrich.CategoryAutomated, SubCategory: "Travel",
	}); err != nil {
		t.Fatalf("UpdateEnrichment: %v", err)
	}

	cands, err := s.ListAttentionCandidates(nil, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("ListAttentionCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0].Message.ID != flight.ID {
		t.Fatalf("ListAttentionCandidates = %d rows, want [flight-1]", len(cands))
	}
	if cands[0].Message.ExtractKind != "" || cands[0].Message.ExtractValue != "" {
		t.Errorf("candidate extract = %q/%q, want empty", cands[0].Message.ExtractKind, cands[0].Message.ExtractValue)
	}
}
