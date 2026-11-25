package sync

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

// Re-audit finding R20: body_truncated was never persisted on the
// server-search path. The brief's stated mechanism was partly wrong —
// BodyUpdate already carries Truncated and UpdateBody already persists
// it — but the search path's save still dropped the flag in three
// places: (1) messageStore.Create never inserted the body_truncated
// column, so the flag buildMessageFromStreamedData computed was lost
// on insert; (2) the search path read the BODY[] literal with a bare
// maxMessageSize limit, so an over-cap message was silently cut to
// exactly the cap with no signal (isBodyTruncated was never consulted
// there); (3) the second UpdateBody in FetchServerMessage (encrypted
// raw body) passed no Truncated value, so it would have cleared a
// persisted flag back to 0. These tests pin the repaired path at the
// builder + store level — the whole search-path save minus the IMAP
// fetch — plus the cap helper both literal readers now share.

func TestCapRawBytes(t *testing.T) {
	// Under the cap: untouched, not truncated.
	raw, truncated := capRawBytes(make([]byte, 1024))
	if truncated || len(raw) != 1024 {
		t.Fatalf("under cap: truncated=%v len=%d, want false/1024", truncated, len(raw))
	}
	// Exactly at the cap: complete — the +1 read distinguishes this.
	raw, truncated = capRawBytes(make([]byte, maxMessageSize))
	if truncated || len(raw) != int(maxMessageSize) {
		t.Fatalf("at cap: truncated=%v len=%d, want false/%d", truncated, len(raw), maxMessageSize)
	}
	// Past the cap: trimmed to the cap and flagged.
	raw, truncated = capRawBytes(make([]byte, maxMessageSize+1))
	if !truncated || len(raw) != int(maxMessageSize) {
		t.Fatalf("over cap: truncated=%v len=%d, want true/%d", truncated, len(raw), maxMessageSize)
	}
}

func TestBuildMessageFromStreamedData_PartOverCapFlaggedTruncated(t *testing.T) {
	e, ms := newEnrichTestEngine(t)
	raw := []byte(overCapTextRaw(t, maxPartSize+100))

	m := e.buildMessageFromStreamedData("acct-1", "folder-1", imap.UID(42), nil, nil, int64(len(raw)), raw, false)
	if !m.BodyTruncated {
		t.Fatal("BodyTruncated=false for a text part 100 bytes over the 10 MB cap (legacy parse)")
	}
	if int64(len(m.BodyText)) != maxPartSize {
		t.Fatalf("BodyText len=%d, want exactly maxPartSize (%d)", len(m.BodyText), maxPartSize)
	}

	// The search path's save: Create must persist the flag (R20 hole 1).
	if err := ms.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := ms.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyTruncated {
		t.Fatal("body_truncated=0 after Create for a truncated search-path body, want 1")
	}
}

func TestBuildMessageFromStreamedData_LiteralOverCapFlaggedTruncated(t *testing.T) {
	e, ms := newEnrichTestEngine(t)
	raw := []byte("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Small\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello\r\n")

	// The literal was cut at the 50 MB cap upstream (capRawBytes in
	// FetchServerMessage); the per-part parse alone sees nothing wrong,
	// so the flag must come from the literal signal (R20 hole 2).
	m := e.buildMessageFromStreamedData("acct-1", "folder-1", imap.UID(43), nil, nil, int64(len(raw)), raw, true)
	if !m.BodyTruncated {
		t.Fatal("BodyTruncated=false when the BODY[] literal was cut at the cap")
	}
	if err := ms.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := ms.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyTruncated {
		t.Fatal("body_truncated=0 after Create for a literal-truncated search-path body, want 1")
	}
}

func TestBuildMessageFromStreamedData_CompleteBodyNotFlagged(t *testing.T) {
	e, ms := newEnrichTestEngine(t)
	raw := []byte("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Small\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello\r\n")

	m := e.buildMessageFromStreamedData("acct-1", "folder-1", imap.UID(44), nil, nil, int64(len(raw)), raw, false)
	if m.BodyTruncated {
		t.Fatal("BodyTruncated=true for a complete small message")
	}
	if err := ms.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := ms.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BodyTruncated {
		t.Fatal("body_truncated=1 after Create for a complete message, want 0")
	}
}

func TestParseMessageBody_PartOverCapFlaggedTruncated(t *testing.T) {
	// Legacy parse (the search path's parser): finding 26 pinned this
	// only for the modern parser, but the search path's flag comes
	// from here.
	_, _, _, truncated := testEngine().parseMessageBody([]byte(overCapTextRaw(t, maxPartSize+100)))
	if !truncated {
		t.Fatal("legacy parse: truncated=false for a text part over the 10 MB cap")
	}
	_, _, _, truncated = testEngine().parseMessageBody([]byte(overCapTextRaw(t, 1024)))
	if truncated {
		t.Fatal("legacy parse: truncated=true for a 1 KB text part")
	}
}
