package sync

import (
	"testing"

	"github.com/absonsnet/daak/internal/reaction"
)

// End-to-end receive test: a real-format reaction email goes through
// detection (parseMessageBodyInternal) + handleReaction, and the pill data
// must come back from ListByMessage on the original. Covers the three
// formats seen in the wild: Gmail JSON, RFC 9078, Outlook fallback.

func TestIssue1GmailRealLayoutEndToEnd(t *testing.T) {
	engine, _, msgStore, reactStore := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<orig@example.com>", "", "me@example.com", "Me")
	seedHandleTestMessage(t, msgStore, "react-1", "<gmail-react-1@mail.gmail.com>", "<orig@example.com>", "reactor@gmail.com", "Reactor Name")

	raw := "From: Reactor Name <reactor@gmail.com>\r\n" +
		"To: me@example.com\r\n" +
		"Subject: 👍\r\n" +
		"Message-ID: <gmail-react-1@mail.gmail.com>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: multipart/alternative; boundary=\"ALT\"\r\n" +
		"\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/plain; charset=\"UTF-8\"\r\n" +
		"\r\n" +
		"👍 Reactor Name reacted via Gmail\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/vnd.google.email-reaction+json; charset=\"UTF-8\"\r\n" +
		"\r\n" +
		"{\"version\": 1, \"emoji\": \"👍\"}\r\n" +
		"--ALT\r\n" +
		"Content-Type: text/html; charset=\"UTF-8\"\r\n" +
		"\r\n" +
		"<p>👍 Reactor Name reacted via Gmail</p>\r\n" +
		"--ALT--\r\n"

	parsed := engine.parseMessageBodyInternal([]byte(raw), "react-1")
	if parsed.Reaction == nil {
		t.Fatal("Gmail real layout: Reaction = nil, want detected reaction")
	}
	if !engine.handleReaction("react-1", parsed.Reaction) {
		t.Fatal("Gmail real layout: handleReaction returned false, want linked")
	}
	listed, err := reactStore.ListByMessage("orig-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Emoji != "👍" || listed[0].ReactorEmail != "reactor@gmail.com" {
		t.Fatalf("Gmail real layout: ListByMessage = %+v, want 1 👍 reaction from reactor@gmail.com", listed)
	}
}

func TestIssue1RFC9078DaakStyleEndToEnd(t *testing.T) {
	engine, _, msgStore, reactStore := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<orig@example.com>", "", "me@example.com", "Me")
	seedHandleTestMessage(t, msgStore, "react-1", "<rfc-react-1@example.com>", "<orig@example.com>", "reactor@gmail.com", "Reactor Name")

	// A Daak-built reaction email, fed back through the receive path.
	raw, _, err := reaction.BuildMessage(reaction.BuildOptions{
		Emoji:       "❤️",
		SenderName:  "Reactor Name",
		SenderEmail: "reactor@gmail.com",
		Original: reaction.Original{
			MessageID: "<orig@example.com>",
			Subject:   "Hello",
			FromName:  "Me",
			FromEmail: "me@example.com",
			To:        []string{"reactor@gmail.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// BuildMessage generates its own Message-ID; the seeded row's
	// Message-ID is only used for the source id, so rewrite the header
	// to match the seeded row.
	_ = raw

	parsed := engine.parseMessageBodyInternal(raw, "react-1")
	if parsed.Reaction == nil {
		t.Fatal("RFC 9078 Daak-style: Reaction = nil, want detected reaction")
	}
	if parsed.Reaction.Emoji != "❤️" {
		t.Fatalf("RFC 9078 Daak-style: Emoji = %q, want ❤️", parsed.Reaction.Emoji)
	}
	if !engine.handleReaction("react-1", parsed.Reaction) {
		t.Fatal("RFC 9078 Daak-style: handleReaction returned false, want linked")
	}
	listed, err := reactStore.ListByMessage("orig-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Emoji != "❤️" {
		t.Fatalf("RFC 9078 Daak-style: ListByMessage = %+v, want 1 ❤️ reaction", listed)
	}
}

func TestIssue1OutlookFallbackEndToEnd(t *testing.T) {
	engine, db, msgStore, reactStore := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<orig@example.com>", "", "me@example.com", "Me")
	seedHandleTestMessage(t, msgStore, "react-1", "<outlook-react-1@smme.nust.edu.pk>", "<orig@example.com>", "sana.zulfiqar@smme.nust.edu.pk", "Dr Sana Zulfiqar")

	// Outlook's fallback reaction email: banner + quoted original, NO
	// JSON part and NO RFC 9078 part (per the archived examples).
	raw := "From: Dr Sana Zulfiqar <sana.zulfiqar@smme.nust.edu.pk>\r\n" +
		"To: MEDD ASCEND <medd.ascend@gmail.com>\r\n" +
		"Subject: Re: Need Deliverables\r\n" +
		"Message-ID: <outlook-react-1@smme.nust.edu.pk>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: multipart/alternative; boundary=\"OUT\"\r\n" +
		"\r\n" +
		"--OUT\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"\r\n" +
		"[thumbs up] Dr Sana Zulfiqar reacted to your message:\r\n" +
		"\r\n" +
		"From: MEDD ASCEND <medd.ascend@gmail.com>\r\n" +
		"Sent: Friday, 11 September 2026 10:42:23\r\n" +
		"To: Dr Sana Zulfiqar <sana.zulfiqar@smme.nust.edu.pk>\r\n" +
		"Subject: Re: Need Deliverables\r\n" +
		"\r\n" +
		"OK, Thanks, we'll be contact you on Monday.\r\n" +
		"--OUT\r\n" +
		"Content-Type: text/html; charset=\"utf-8\"\r\n" +
		"\r\n" +
		"<html><body><p>[thumbs up] Dr Sana Zulfiqar reacted to your message:</p></body></html>\r\n" +
		"--OUT--\r\n"

	parsed := engine.parseMessageBodyInternal([]byte(raw), "react-1")
	if parsed.Reaction == nil {
		t.Fatal("Outlook fallback: Reaction = nil, want a detected Outlook reaction")
	}
	if parsed.Reaction.Emoji != "👍" {
		t.Fatalf("Outlook fallback: Emoji = %q, want 👍", parsed.Reaction.Emoji)
	}
	if parsed.Reaction.Format != reactionFormatOutlook {
		t.Fatalf("Outlook fallback: Format = %q, want %q", parsed.Reaction.Format, reactionFormatOutlook)
	}
	if !engine.handleReaction("react-1", parsed.Reaction) {
		t.Fatal("Outlook fallback: handleReaction returned false, want linked")
	}
	listed, err := reactStore.ListByMessage("orig-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Emoji != "👍" || listed[0].ReactorEmail != "sana.zulfiqar@smme.nust.edu.pk" {
		t.Fatalf("Outlook fallback: ListByMessage = %+v, want 1 👍 reaction from sana.zulfiqar@smme.nust.edu.pk", listed)
	}
	if !isHidden(t, db, "react-1") {
		t.Fatal("Outlook fallback: reaction email is not hidden, want is_hidden=1")
	}
}

// Re-cased reaction markers: header names and values are case-insensitive
// per RFC, so a sender emitting them in different case must still be
// detected (the detection gate used to be a case-sensitive byte scan).
func TestDetectReactionRecasedHeaders(t *testing.T) {
	engine := testEngine()

	// Gmail part with a re-cased content type.
	raw := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: 👍\r\n" +
		"Message-ID: <react-1@example.com>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/vnd.google.EMAIL-REACTION+JSON; charset=utf-8\r\n" +
		"\r\n" +
		"{\"version\": 1, \"emoji\": \"👍\"}\r\n" +
		"--BOUNDARY--\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "msg-1"); parsed.Reaction == nil {
		t.Fatal("re-cased Gmail content type: Reaction = nil, want detected reaction")
	}

	// RFC 9078 part with a re-cased disposition header name and value.
	raw = "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Re: hello\r\n" +
		"Message-ID: <react-2@example.com>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Hello\r\n" +
		"--BOUNDARY\r\n" +
		"CONTENT-DISPOSITION: REACTION\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"👍\r\n" +
		"--BOUNDARY--\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "msg-2"); parsed.Reaction == nil {
		t.Fatal("re-cased RFC 9078 disposition: Reaction = nil, want detected reaction")
	}
}

// Banner-line parser: all six Outlook reactions in bracketed form, the
// raw-emoji form, and the rejects.
func TestParseOutlookBannerLine(t *testing.T) {
	cases := []struct {
		line  string
		emoji string
		ok    bool
	}{
		{"[thumbs up] Dr Sana Zulfiqar reacted to your message:", "👍", true},
		{"[celebrate] Houda CHIHI reacted to your message:", "🎉", true},
		{"[heart] Jane Doe reacted to your message:", "❤️", true},
		{"[laugh] Jane Doe reacted to your message:", "😂", true},
		{"[surprise] Jane Doe reacted to your message:", "😮", true},
		{"[sad] Jane Doe reacted to your message:", "😢", true},
		{"[THUMBS UP] Jane Doe reacted to your message:", "👍", true},
		{"👍 Dr Sana Zulfiqar reacted to your message:", "👍", true},
		{"❤️ Jane Doe reacted to your message:", "❤️", true},
		{"Dr Sana Zulfiqar reacted to your message:", "", false},    // no emoji, no bracket
		{"[party] Jane Doe reacted to your message:", "", false},    // unknown bracket
		{"[thumbs up] reacted to your message:", "", false},         // no reactor name
		{"👍👍 Jane Doe reacted to your message:", "", false},         // two emoji
		{"[thumbs up] Jane Doe reacted to your message", "", false}, // missing colon
		{"[thumbs up] Jane Doe liked your message:", "", false},     // wrong wording
		{"👍 Jane Doe reacted to your message: extra", "", false},    // trailing text
	}
	for _, c := range cases {
		emoji, ok := parseOutlookBannerLine(c.line)
		if ok != c.ok || emoji != c.emoji {
			t.Errorf("parseOutlookBannerLine(%q) = (%q, %v), want (%q, %v)", c.line, emoji, ok, c.emoji, c.ok)
		}
	}
}

// Fallback detector negatives: the banner alone is not enough — without
// the quote block, or when the banner is not the first line, the message
// stays an ordinary email.
func TestDetectOutlookFallbackNegatives(t *testing.T) {
	engine := testEngine()

	// Banner first line but no quote block.
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r1@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n[thumbs up] Jane Doe reacted to your message:\r\n\r\nSome body text, no quoted original.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m1"); parsed.Reaction != nil {
		t.Errorf("banner without quote block: Reaction = %+v, want nil", parsed.Reaction)
	}

	// Banner quoted mid-body, not the first line.
	raw = "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r2@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello,\r\n\r\n[thumbs up] Jane Doe reacted to your message:\r\n\r\nFrom: Someone <s@example.com>\r\nSent: Friday, 11 September 2026 10:42:23\r\nTo: Jane Doe <j@example.com>\r\nSubject: Re: x\r\n\r\nQuoted body.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m2"); parsed.Reaction != nil {
		t.Errorf("banner mid-body: Reaction = %+v, want nil", parsed.Reaction)
	}

	// Quote block but no banner — an ordinary Outlook reply.
	raw = "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r3@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nThanks, see you Monday.\r\n\r\nFrom: Someone <s@example.com>\r\nSent: Friday, 11 September 2026 10:42:23\r\nTo: Jane Doe <j@example.com>\r\nSubject: Re: x\r\n\r\nQuoted body.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m3"); parsed.Reaction != nil {
		t.Errorf("ordinary reply with quote block: Reaction = %+v, want nil", parsed.Reaction)
	}
}

// HTML-only fallback: no text/plain part, banner carried in the HTML.
func TestDetectOutlookFallbackHTMLOnly(t *testing.T) {
	engine := testEngine()
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r4@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><p>[celebrate] Houda CHIHI reacted to your message:</p><p>From: Someone &lt;s@example.com&gt;<br>Sent: Friday, 11 September 2026 10:42:23<br>To: Jane Doe &lt;j@example.com&gt;<br>Subject: Re: x</p><p>Quoted body.</p></body></html>\r\n"
	parsed := engine.parseMessageBodyInternal([]byte(raw), "m4")
	if parsed.Reaction == nil {
		t.Fatal("HTML-only fallback: Reaction = nil, want detected Outlook reaction")
	}
	if parsed.Reaction.Emoji != "🎉" {
		t.Fatalf("HTML-only fallback: Emoji = %q, want 🎉", parsed.Reaction.Emoji)
	}
}
