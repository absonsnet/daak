package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/sendqueue"
)

// --- Unsubscribe tests -----------------------------------------------------
// Real DB-backed stores plus a fake transport (same approach as the
// reaction tests) and a local TLS server standing in for the sender's
// one-click endpoint, so the whole click → POST/record flow runs
// without a network.

type unsubscribeTestApp struct {
	app       *App
	db        *database.DB
	sent      []*capturedSend
	savedSent [][]byte
	uidSeq    int
}

func newUnsubscribeTestApp(t *testing.T) *unsubscribeTestApp {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username) VALUES ('acc-1', 'Me', 'me@example.com', 'imap.example.com', 'smtp.example.com', 'me@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO identities (id, account_id, email, name, is_default) VALUES ('ident-1', 'acc-1', 'me@example.com', 'Me', 1)`); err != nil {
		t.Fatal(err)
	}
	for _, f := range [][3]string{
		{"inbox-1", "INBOX", "inbox"},
		{"spam-1", "Spam", "spam"},
		{"sent-1", "Sent", "sent"},
		{"trash-1", "Trash", "trash"},
	} {
		if _, err := db.Exec(`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES (?, 'acc-1', ?, ?, ?)`, f[0], f[1], f[1], f[2]); err != nil {
			t.Fatal(err)
		}
	}

	uta := &unsubscribeTestApp{db: db}
	accountStore := account.NewStore(db)
	a := &App{
		ctx:             context.Background(),
		db:              db,
		accountStore:    accountStore,
		folderStore:     folder.NewStore(db),
		messageStore:    message.NewStore(db),
		ownExpungeAt:    make(map[string]time.Time),
		syncLastRequest: make(map[string]time.Time),
		syncContexts:    make(map[string]context.CancelFunc),
		composeOps: composeOps{
			accountStore: accountStore,
			rawSendOverride: func(_ context.Context, from string, recipients []string, raw []byte) error {
				uta.sent = append(uta.sent, &capturedSend{from: from, recipients: recipients, raw: raw})
				return nil
			},
			sentSaveOverride: func(_ context.Context, _ string, raw []byte) error {
				uta.savedSent = append(uta.savedSent, raw)
				return nil
			},
		},
	}
	uta.app = a
	return uta
}

// seedMessage inserts a message row with List-Unsubscribe headers.
func (uta *unsubscribeTestApp) seedMessage(t *testing.T, id, folderID, fromEmail, lu string, post bool) {
	t.Helper()
	uta.uidSeq++
	_, err := uta.db.Exec(`INSERT INTO messages (id, account_id, folder_id, uid, message_id, subject, from_name, from_email, to_list, date, list_unsubscribe, list_unsubscribe_post) VALUES (?, 'acc-1', ?, ?, ?, 'Newsletter', 'News', ?, ?, ?, ?, ?)`,
		id, folderID, uta.uidSeq, "<"+id+"@example.com>", fromEmail, addrListJSON(t, "me@example.com"), time.Now().UTC(), lu, post)
	if err != nil {
		t.Fatal(err)
	}
}

func (uta *unsubscribeTestApp) deps(client *http.Client) *unsubscribeDeps {
	d := uta.app.unsubscribeDeps()
	d.httpClient = client
	return d
}

func TestUnsubscribeOneClick(t *testing.T) {
	uta := newUnsubscribeTestApp(t)

	var gotMethod, gotBody string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	uta.seedMessage(t, "msg-1", "inbox-1", "news@example.com",
		"<"+srv.URL+"/unsub?id=1>, <mailto:unsub@example.com?subject=unsub-1>", true)

	res, err := uta.deps(srv.Client()).unsubscribeFromMessage(context.Background(), "msg-1")
	if err != nil {
		t.Fatalf("unsubscribeFromMessage() error = %v", err)
	}
	if res.Method != "one_click" {
		t.Fatalf("Method = %q, want one_click", res.Method)
	}
	if gotMethod != http.MethodPost || gotBody != "List-Unsubscribe=One-Click" {
		t.Fatalf("server saw %s %q, want POST List-Unsubscribe=One-Click", gotMethod, gotBody)
	}
	// The local test server (127.0.0.1) is unrelated to the sender
	// domain, so the mismatch flag is correctly raised even though
	// the POST itself succeeded.
	if !res.DomainMismatch {
		t.Fatalf("DomainMismatch = false, want true (127.0.0.1 is not example.com)")
	}
	// Recorded, so the button is suppressed afterwards.
	if has, err := uta.app.messageStore.HasUnsubscribed("news@example.com", "acc-1"); err != nil || !has {
		t.Fatalf("HasUnsubscribed = %v, err = %v, want true", has, err)
	}
	stList := uta.deps(nil).getStatuses([]string{"msg-1"})
	if len(stList) != 1 {
		t.Fatalf("getStatuses = %d, want 1", len(stList))
	}
	st := stList[0]
	if st == nil || !st.AlreadyUnsubscribed {
		t.Fatalf("status after one-click = %+v, want alreadyUnsubscribed", st)
	}
	// A second click is idempotent: no second POST.
	res2, err := uta.deps(srv.Client()).unsubscribeFromMessage(context.Background(), "msg-1")
	if err != nil {
		t.Fatalf("second unsubscribeFromMessage() error = %v", err)
	}
	if res2.Method != "already" {
		t.Fatalf("second click Method = %q, want already", res2.Method)
	}
}

func TestUnsubscribeHTTPSPlainOpensInBrowser(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.seedMessage(t, "msg-1", "inbox-1", "news@example.com", "<https://news.example.com/unsub?id=1>", false)

	res, err := uta.deps(nil).unsubscribeFromMessage(context.Background(), "msg-1")
	if err != nil {
		t.Fatalf("unsubscribeFromMessage() error = %v", err)
	}
	if res.Method != "browser" || res.URL != "https://news.example.com/unsub?id=1" {
		t.Fatalf("result = %+v, want browser + URL", res)
	}
	// Recorded as a browser action, but NOT a completed unsubscribe:
	// the button keeps being offered.
	if has, err := uta.app.messageStore.HasUnsubscribed("news@example.com", "acc-1"); err != nil || has {
		t.Fatalf("HasUnsubscribed after browser = %v, err = %v, want false", has, err)
	}
	if method, ok, _ := uta.app.messageStore.UnsubscribeActionMethod("news@example.com", "acc-1"); !ok || method != "browser" {
		t.Fatalf("recorded method = %q, %v, want browser", method, ok)
	}
}

func TestUnsubscribeHTTPDegradesToBrowser(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	// Plain http even WITH the one-click marker: no silent POST, the
	// URL comes back for the browser instead.
	uta.seedMessage(t, "msg-1", "inbox-1", "news@example.com", "<http://news.example.com/unsub?id=1>", true)

	res, err := uta.deps(nil).unsubscribeFromMessage(context.Background(), "msg-1")
	if err != nil {
		t.Fatalf("unsubscribeFromMessage() error = %v", err)
	}
	if res.Method != "browser" || res.URL != "http://news.example.com/unsub?id=1" {
		t.Fatalf("result = %+v, want browser downgrade", res)
	}
}

func TestUnsubscribeMailto(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.seedMessage(t, "msg-1", "inbox-1", "deals@shop.example", "<mailto:unsub@shop.example?subject=unsub-42>", false)

	res, err := uta.deps(nil).unsubscribeFromMessage(context.Background(), "msg-1")
	if err != nil {
		t.Fatalf("unsubscribeFromMessage() error = %v", err)
	}
	if res.Method != "mailto" {
		t.Fatalf("Method = %q, want mailto", res.Method)
	}
	if len(uta.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(uta.sent))
	}
	sent := uta.sent[0]
	if sent.from != "me@example.com" || len(sent.recipients) != 1 || sent.recipients[0] != "unsub@shop.example" {
		t.Fatalf("sent from=%q to=%v, want me@example.com -> unsub@shop.example", sent.from, sent.recipients)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(sent.raw)))
	if err != nil {
		t.Fatalf("mail.ReadMessage: %v", err)
	}
	if got := msg.Header.Get("Subject"); got != "unsub-42" {
		t.Errorf("Subject = %q, want unsub-42 (from the header)", got)
	}
	if got := msg.Header.Get("To"); got != "unsub@shop.example" {
		t.Errorf("To = %q, want unsub@shop.example", got)
	}
	// Filed in Sent (provider does not auto-save).
	if len(uta.savedSent) != 1 {
		t.Fatalf("saved to Sent %d, want 1", len(uta.savedSent))
	}
	// Recorded as completed.
	if has, err := uta.app.messageStore.HasUnsubscribed("deals@shop.example", "acc-1"); err != nil || !has {
		t.Fatalf("HasUnsubscribed = %v, err = %v, want true", has, err)
	}
}

// TestUnsubscribeMailtoSentSaveFailureLeavesWarning (fourth-audit
// finding 46): the unsubscribe email is delivered but its Sent copy
// cannot be saved — the failure must leave the same persistent
// "sent but not saved" Outbox warning a normal send leaves, not
// just a log line.
func TestUnsubscribeMailtoSentSaveFailureLeavesWarning(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.app.composeOps.sentSaveOverride = func(_ context.Context, _ string, _ []byte) error {
		return fmt.Errorf("IMAP APPEND refused")
	}
	// Pre-install the queue service with no Wails emitter (the
	// sendqueue harness pattern): the unsubscribeDeps closure must
	// still be the real one — ensureSendQueueService returns this
	// service — but a bare test App has no Wails context, and the
	// runtime's EventsEmit fatals on it.
	uta.app.sendQueue = &sendQueueService{store: sendqueue.NewStore(uta.db), ctx: context.Background()}
	uta.seedMessage(t, "msg-1", "inbox-1", "deals@shop.example", "<mailto:unsub@shop.example?subject=unsub-42>", false)

	if _, err := uta.deps(nil).unsubscribeFromMessage(context.Background(), "msg-1"); err != nil {
		t.Fatalf("unsubscribeFromMessage() error = %v", err)
	}
	// The mail was still delivered.
	if len(uta.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(uta.sent))
	}
	var n int
	if err := uta.db.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE status = 'sent_unsaved'`).Scan(&n); err != nil {
		t.Fatalf("count sent_unsaved rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("sent_unsaved rows = %d, want 1 (persistent Sent-save warning)", n)
	}
	var subject, recipient, lastErr string
	if err := uta.db.QueryRow(`SELECT subject, recipient, last_error FROM send_queue WHERE status = 'sent_unsaved'`).Scan(&subject, &recipient, &lastErr); err != nil {
		t.Fatalf("read warning row: %v", err)
	}
	if subject != "unsub-42" || recipient != "unsub@shop.example" {
		t.Errorf("warning row subject/recipient = %q/%q, want unsub-42/unsub@shop.example", subject, recipient)
	}
	if !strings.Contains(lastErr, "IMAP APPEND refused") {
		t.Errorf("warning last_error = %q, want it to name the save failure", lastErr)
	}
}

func TestUnsubscribeStatusRules(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.seedMessage(t, "inbox-1", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true)
	uta.seedMessage(t, "spam-1", "spam-1", "spammy@example.com", "<https://spammy.example.com/unsub>", false)
	uta.seedMessage(t, "sent-1", "sent-1", "news@example.com", "<https://news.example.com/unsub>", true)
	uta.seedMessage(t, "plain-1", "inbox-1", "friend@example.com", "", false)
	uta.seedMessage(t, "mismatch-1", "inbox-1", "news@example.com", "<https://tracker.other.com/unsub>", false)

	stList := uta.deps(nil).getStatuses([]string{"inbox-1", "spam-1", "sent-1", "plain-1", "mismatch-1"})
	st := make(map[string]*UnsubscribeStatus, len(stList))
	for _, s := range stList {
		st[s.MessageID] = s
	}
	if !st["inbox-1"].Available || st["inbox-1"].Method != "one_click" {
		t.Errorf("inbox status = %+v, want available one_click", st["inbox-1"])
	}
	if !st["spam-1"].Available || !st["spam-1"].IsSpam {
		t.Errorf("spam status = %+v, want available + isSpam", st["spam-1"])
	}
	if st["sent-1"].Available {
		t.Errorf("sent status = %+v, want unavailable (Sent never offers unsubscribe)", st["sent-1"])
	}
	if st["plain-1"].Available {
		t.Errorf("no-header status = %+v, want unavailable", st["plain-1"])
	}
	if !st["mismatch-1"].DomainMismatch {
		t.Errorf("mismatch status = %+v, want domainMismatch", st["mismatch-1"])
	}
	if st["inbox-1"].DomainMismatch {
		t.Errorf("same-domain status = %+v, want no mismatch", st["inbox-1"])
	}
}

func TestUnsubscribeFollowUp(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.seedMessage(t, "msg-1", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true)
	uta.seedMessage(t, "msg-2", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true)
	uta.seedMessage(t, "msg-3", "inbox-1", "other@example.com", "<https://other.example.com/unsub>", false)

	// The real App.Trash emits Wails events, which need a Wails
	// context and would kill the test process — the follow-up is
	// tested with the local move the trash step performs, and the
	// full Trash path is covered by the Xvfb smoke test.
	d := uta.deps(nil)
	d.trash = func(ids []string) (bool, error) {
		return true, uta.app.messageStore.MoveMessages(ids, "trash-1")
	}
	if err := d.followUp("msg-1", true, true); err != nil {
		t.Fatalf("followUp() error = %v", err)
	}
	// Both of the sender's inbox messages moved to Trash; the other
	// sender's mail stayed put.
	for _, id := range []string{"msg-1", "msg-2"} {
		m, err := uta.app.messageStore.Get(id)
		if err != nil || m == nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if m.FolderID != "trash-1" {
			t.Errorf("message %s folder = %q, want trash-1", id, m.FolderID)
		}
	}
	m, _ := uta.app.messageStore.Get("msg-3")
	if m.FolderID != "inbox-1" {
		t.Errorf("other sender folder = %q, want inbox-1", m.FolderID)
	}
	// Auto-delete rule created.
	if has, err := uta.app.messageStore.HasSenderRule("news@example.com", "acc-1", "auto_trash"); err != nil || !has {
		t.Fatalf("HasSenderRule = %v, err = %v, want true", has, err)
	}
}

func TestUnsubscribeNoHeaders(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.seedMessage(t, "msg-1", "inbox-1", "friend@example.com", "", false)
	if _, err := uta.deps(nil).unsubscribeFromMessage(context.Background(), "msg-1"); err == nil {
		t.Fatal("unsubscribeFromMessage() succeeded without headers, want error")
	}
}

func TestGetSubscriptions(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	now := time.Now().UTC()
	uid := 200
	seed := func(id, folderID, from, lu string, post bool, date time.Time) {
		t.Helper()
		uid++
		_, err := uta.db.Exec(`INSERT INTO messages (id, account_id, folder_id, uid, message_id, subject, from_name, from_email, to_list, date, list_unsubscribe, list_unsubscribe_post) VALUES (?, 'acc-1', ?, ?, ?, 'Newsletter', 'News', ?, ?, ?, ?, ?)`,
			id, folderID, uid, "<"+id+"@example.com>", from, addrListJSON(t, "me@example.com"), date, lu, post)
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("s1", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true, now.Add(-1*time.Hour))
	seed("s2", "inbox-1", "NEWS@example.com", "<https://news.example.com/unsub>", true, now.Add(-2*time.Hour))
	seed("s3", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true, now.Add(-40*24*time.Hour))
	seed("s4", "inbox-1", "deals@shop.example", "<https://tracker.other.com/unsub>", false, now.Add(-3*time.Hour))
	seed("s5", "inbox-1", "friend@example.com", "", false, now.Add(-4*time.Hour))
	if err := uta.app.messageStore.RecordUnsubscribeAction("deals@shop.example", "acc-1", "mailto"); err != nil {
		t.Fatal(err)
	}
	if err := uta.app.messageStore.SetSenderRule("news@example.com", "acc-1", "auto_trash"); err != nil {
		t.Fatal(err)
	}

	subs, err := uta.app.GetSubscriptions()
	if err != nil {
		t.Fatalf("GetSubscriptions() error = %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("GetSubscriptions() = %d senders, want 2", len(subs))
	}
	top := subs[0]
	if top.SenderEmail != "news@example.com" || top.CountAll != 3 || top.Count30Days != 2 || !top.OneClick {
		t.Fatalf("top = %+v, want news@example.com 3/2 one-click", top)
	}
	if top.DomainMismatch {
		t.Errorf("top.DomainMismatch = true, want false (same domain)")
	}
	if !top.AutoDelete {
		t.Errorf("top.AutoDelete = false, want true (rule set)")
	}
	if top.Unsubscribed {
		t.Errorf("top.Unsubscribed = true, want false (no action recorded)")
	}
	second := subs[1]
	if second.SenderEmail != "deals@shop.example" || !second.Unsubscribed || second.UnsubscribeMethod != "mailto" {
		t.Fatalf("second = %+v, want deals@shop.example unsubscribed via mailto", second)
	}
	if !second.DomainMismatch {
		t.Errorf("second.DomainMismatch = false, want true (tracker.other.com)")
	}

	// Unsubscribe from the view uses the sender's newest message. The
	// news sender's one-click endpoint is a real host, so exercise the
	// view path against a local TLS server sender instead.
	var viewPosts int
	viewSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viewPosts++
		w.WriteHeader(http.StatusOK)
	}))
	defer viewSrv.Close()
	seed("s6", "inbox-1", "local@test.example", "<"+viewSrv.URL+"/unsub>", true, now)
	res, err := uta.deps(viewSrv.Client()).unsubscribeFromSubscription(context.Background(), "local@test.example", "acc-1")
	if err != nil {
		t.Fatalf("UnsubscribeFromSubscription() error = %v", err)
	}
	if res.Method != "one_click" || viewPosts != 1 {
		t.Fatalf("Method = %q, posts = %d, want one_click/1", res.Method, viewPosts)
	}
}

func TestSetSenderAutoDelete(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	if err := uta.app.SetSenderAutoDelete("news@example.com", "acc-1", true); err != nil {
		t.Fatalf("SetSenderAutoDelete(true): %v", err)
	}
	if has, _ := uta.app.messageStore.HasSenderRule("news@example.com", "acc-1", "auto_trash"); !has {
		t.Fatal("rule missing after enable")
	}
	if err := uta.app.SetSenderAutoDelete("news@example.com", "acc-1", false); err != nil {
		t.Fatalf("SetSenderAutoDelete(false): %v", err)
	}
	if has, _ := uta.app.messageStore.HasSenderRule("news@example.com", "acc-1", "auto_trash"); has {
		t.Fatal("rule still present after disable")
	}
}

// TestApplySenderRules checks the sync-time rule application: inbox mail
// from a ruled sender moves to Trash, other mail and other folders are
// untouched, and a missing rule set is a no-op.
func TestApplySenderRules(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	uta.seedMessage(t, "msg-1", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true)
	uta.seedMessage(t, "msg-2", "inbox-1", "news@example.com", "<https://news.example.com/unsub>", true)
	uta.seedMessage(t, "msg-3", "inbox-1", "other@example.com", "<https://other.example.com/unsub>", false)
	uta.seedMessage(t, "msg-4", "spam-1", "news@example.com", "<https://news.example.com/unsub>", true)

	d := uta.deps(nil)
	d.trash = func(ids []string) (bool, error) {
		return true, uta.app.messageStore.MoveMessages(ids, "trash-1")
	}

	// No rules yet: nothing moves.
	d.applyRules("acc-1", "inbox-1")
	if m, _ := uta.app.messageStore.Get("msg-1"); m.FolderID != "inbox-1" {
		t.Fatalf("msg-1 moved without any rule: %q", m.FolderID)
	}

	if err := uta.app.messageStore.SetSenderRule("news@example.com", "acc-1", "auto_trash"); err != nil {
		t.Fatal(err)
	}
	d.applyRules("acc-1", "inbox-1")
	for _, id := range []string{"msg-1", "msg-2"} {
		if m, _ := uta.app.messageStore.Get(id); m.FolderID != "trash-1" {
			t.Errorf("message %s folder = %q, want trash-1", id, m.FolderID)
		}
	}
	if m, _ := uta.app.messageStore.Get("msg-3"); m.FolderID != "inbox-1" {
		t.Errorf("other sender folder = %q, want inbox-1", m.FolderID)
	}
	// The spam copy is not in the synced folder, so the rule leaves it.
	if m, _ := uta.app.messageStore.Get("msg-4"); m.FolderID != "spam-1" {
		t.Errorf("spam copy folder = %q, want spam-1", m.FolderID)
	}
	// Non-inbox folders never apply rules.
	d.applyRules("acc-1", "spam-1")
	if m, _ := uta.app.messageStore.Get("msg-4"); m.FolderID != "spam-1" {
		t.Errorf("spam copy folder after spam apply = %q, want spam-1", m.FolderID)
	}
}
