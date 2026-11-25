package sync

import (
	"testing"
)

// R26: Outlook reaction detection only understood English banners and a
// rigid quote block, and a folded reaction header broke the detection
// gate. These tests cover the non-English banner path (German/French,
// bracketed and raw-emoji forms, with localised quote blocks), folded
// reaction headers (gate + MIME part checks), and the negatives that
// prove the quote-block guard still holds.
//
// The German/French banner wordings follow Outlook's localised pattern
// (bracketed local reaction name, reactor name, local verb phrase,
// trailing colon); the detector does not depend on the verb wording, only
// on the bracketed token resolving through the known reaction names plus
// the banner shape and the quote block that must follow.

const reauditR26GermanQuoteBlock = "Von: Max Mustermann <max@example.com>\r\n" +
	"Gesendet: Freitag, 11. September 2026 10:42:23\r\n" +
	"An: Jane Doe <jane@example.com>\r\n" +
	"Betreff: Re: Need Deliverables\r\n"

const reauditR26FrenchQuoteBlock = "De : Marie Dupont <marie@example.com>\r\n" +
	"Envoyé : vendredi 11 septembre 2026 10:42:23\r\n" +
	"À : Jane Doe <jane@example.com>\r\n" +
	"Objet : Re: Need Deliverables\r\n"

const reauditR26EnglishQuoteBlock = "From: Someone <s@example.com>\r\n" +
	"Sent: Friday, 11 September 2026 10:42:23\r\n" +
	"To: Jane Doe <j@example.com>\r\n" +
	"Subject: Re: x\r\n"

// TestReauditR26GermanOutlookFallbackEndToEnd is the German fallback
// reaction email all the way through detection + handleReaction: the
// banner is German, the quote block is German, and the reaction must
// still link to the original and hide the reaction email.
func TestReauditR26GermanOutlookFallbackEndToEnd(t *testing.T) {
	engine, db, msgStore, reactStore := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<orig@example.com>", "", "me@example.com", "Me")
	seedHandleTestMessage(t, msgStore, "react-1", "<outlook-react-de@example.com>", "<orig@example.com>", "max@example.com", "Max Mustermann")

	raw := "From: Max Mustermann <max@example.com>\r\n" +
		"To: me@example.com\r\n" +
		"Subject: Re: Need Deliverables\r\n" +
		"Message-ID: <outlook-react-de@example.com>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"[Daumen hoch] Max Mustermann hat auf Ihre Nachricht reagiert:\r\n" +
		"\r\n" +
		reauditR26GermanQuoteBlock +
		"\r\n" +
		"OK, danke, wir melden uns am Montag.\r\n"

	parsed := engine.parseMessageBodyInternal([]byte(raw), "react-1")
	if parsed.Reaction == nil {
		t.Fatal("German Outlook fallback: Reaction = nil, want a detected Outlook reaction")
	}
	if parsed.Reaction.Emoji != "👍" {
		t.Fatalf("German Outlook fallback: Emoji = %q, want 👍", parsed.Reaction.Emoji)
	}
	if parsed.Reaction.Format != reactionFormatOutlook {
		t.Fatalf("German Outlook fallback: Format = %q, want %q", parsed.Reaction.Format, reactionFormatOutlook)
	}
	if !engine.handleReaction("react-1", parsed.Reaction) {
		t.Fatal("German Outlook fallback: handleReaction returned false, want linked")
	}
	listed, err := reactStore.ListByMessage("orig-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Emoji != "👍" || listed[0].ReactorEmail != "max@example.com" {
		t.Fatalf("German Outlook fallback: ListByMessage = %+v, want 1 👍 reaction from max@example.com", listed)
	}
	if !isHidden(t, db, "react-1") {
		t.Fatal("German Outlook fallback: reaction email is not hidden, want is_hidden=1")
	}
}

// TestReauditR26FrenchOutlookFallback: French bracketed banner (with the
// French space before the colon) plus a French quote block.
func TestReauditR26FrenchOutlookFallback(t *testing.T) {
	engine := testEngine()
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r26fr@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n[J'aime] Marie Dupont a réagi à votre message :\r\n\r\n" +
		reauditR26FrenchQuoteBlock +
		"\r\nCorps cité.\r\n"
	parsed := engine.parseMessageBodyInternal([]byte(raw), "m26fr")
	if parsed.Reaction == nil {
		t.Fatal("French Outlook fallback: Reaction = nil, want a detected Outlook reaction")
	}
	if parsed.Reaction.Emoji != "👍" {
		t.Fatalf("French Outlook fallback: Emoji = %q, want 👍", parsed.Reaction.Emoji)
	}
	if parsed.Reaction.Format != reactionFormatOutlook {
		t.Fatalf("French Outlook fallback: Format = %q, want %q", parsed.Reaction.Format, reactionFormatOutlook)
	}
}

// TestReauditR26NonEnglishEmojiBanner: the raw-emoji banner form is
// language-independent — the emoji is the marker, so the verb wording
// in any language matches.
func TestReauditR26NonEnglishEmojiBanner(t *testing.T) {
	engine := testEngine()
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r26em@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n👍 Max Mustermann hat auf Ihre Nachricht reagiert:\r\n\r\n" +
		reauditR26GermanQuoteBlock +
		"\r\nOK, danke.\r\n"
	parsed := engine.parseMessageBodyInternal([]byte(raw), "m26em")
	if parsed.Reaction == nil {
		t.Fatal("non-English emoji banner: Reaction = nil, want a detected Outlook reaction")
	}
	if parsed.Reaction.Emoji != "👍" {
		t.Fatalf("non-English emoji banner: Emoji = %q, want 👍", parsed.Reaction.Emoji)
	}
}

// TestParseOutlookBannerLineGeneric pins the shape-only banner parser:
// known bracketed names (English + localised) and the raw-emoji form
// with any verb wording, plus the rejects.
func TestParseOutlookBannerLineGeneric(t *testing.T) {
	cases := []struct {
		line  string
		emoji string
		ok    bool
	}{
		{"[Daumen hoch] Max Mustermann hat auf Ihre Nachricht reagiert:", "👍", true},
		{"[Herz] Max Mustermann hat auf Ihre Nachricht reagiert:", "❤️", true},
		{"[Traurig] Max Mustermann hat auf Ihre Nachricht reagiert:", "😢", true},
		{"[J'aime] Marie Dupont a réagi à votre message :", "👍", true},
		{"[J’aime] Marie Dupont a réagi à votre message :", "👍", true}, // curly apostrophe
		{"[Cœur] Marie Dupont a réagi à votre message :", "❤️", true},
		{"[thumbs up] Jane Doe hat auf Ihre Nachricht reagiert:", "👍", true}, // English token, foreign words
		{"👍 Max Mustermann hat auf Ihre Nachricht reagiert:", "👍", true},
		{"❤️ Marie Dupont a réagi à votre message :", "❤️", true},
		{"[Unbekannt] Max Mustermann hat auf Ihre Nachricht reagiert:", "", false},  // unknown bracketed name: no emoji to map to
		{"[party] Jane Doe hat auf Ihre Nachricht reagiert:", "", false},            // unknown bracketed name
		{"[Daumen hoch] Max:", "", false},                                           // reactor name but no verb words
		{"[Daumen hoch] Max Mustermann hat auf Ihre Nachricht reagiert", "", false}, // missing colon
		{"[Daumen hoch] Max Mustermann hat auf Ihre Nachricht reagiert: extra", "", false},
		{"[] Max Mustermann hat auf Ihre Nachricht reagiert:", "", false}, // empty token
		{"👍 Max:", "", false}, // emoji but no verb words
		{"👍👍 Max Mustermann hat auf Ihre Nachricht reagiert:", "", false}, // two emoji
		{"Max Mustermann hat auf Ihre Nachricht reagiert:", "", false},    // no marker at all
	}
	for _, c := range cases {
		emoji, ok := parseOutlookBannerLineGeneric(c.line)
		if ok != c.ok || emoji != c.emoji {
			t.Errorf("parseOutlookBannerLineGeneric(%q) = (%q, %v), want (%q, %v)", c.line, emoji, ok, c.emoji, c.ok)
		}
	}
}

// TestReauditR26FoldedGmailContentType: the Gmail reaction content type
// folded mid-token must still be detected — the folding-aware gate has
// to pass, and the part check has to cope with go-message joining the
// folded segments with a space.
func TestReauditR26FoldedGmailContentType(t *testing.T) {
	engine := testEngine()
	raw := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: 👍\r\n" +
		"Message-ID: <react-1@example.com>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
		"\r\n" +
		"--BOUNDARY\r\n" +
		"Content-Type: text/vnd.google.email-\r\n" +
		" reaction+json; charset=utf-8\r\n" +
		"\r\n" +
		"{\"version\": 1, \"emoji\": \"👍\"}\r\n" +
		"--BOUNDARY--\r\n"

	parsed := engine.parseMessageBodyInternal([]byte(raw), "msg-1")
	if parsed.Reaction == nil {
		t.Fatal("folded Gmail content type: Reaction = nil, want a detected Gmail reaction")
	}
	if parsed.Reaction.Emoji != "👍" {
		t.Errorf("folded Gmail content type: Emoji = %q, want 👍", parsed.Reaction.Emoji)
	}
	if parsed.Reaction.Format != reactionFormatGmail {
		t.Errorf("folded Gmail content type: Format = %q, want %q", parsed.Reaction.Format, reactionFormatGmail)
	}
}

// TestReauditR26FoldedDisposition covers both fold positions for the RFC
// 9078 marker: a fold at the word boundary (after the colon) and a fold
// inside the word "reaction" itself.
func TestReauditR26FoldedDisposition(t *testing.T) {
	cases := []struct {
		name        string
		disposition string
	}{
		{"fold after colon", "Content-Disposition:\r\n reaction\r\n"},
		{"fold inside word", "Content-Disposition: react\r\n ion\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "From: alice@example.com\r\n" +
				"To: bob@example.com\r\n" +
				"Subject: Re: Hello\r\n" +
				"Message-ID: <react-1@example.com>\r\n" +
				"In-Reply-To: <orig@example.com>\r\n" +
				"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
				"\r\n" +
				"--BOUNDARY\r\n" +
				"Content-Type: text/plain; charset=utf-8\r\n" +
				tc.disposition +
				"\r\n" +
				"🎉\r\n" +
				"--BOUNDARY--\r\n"
			parsed := testEngine().parseMessageBodyInternal([]byte(raw), "msg-1")
			if parsed.Reaction == nil {
				t.Fatal("folded disposition: Reaction = nil, want a detected RFC 9078 reaction")
			}
			if parsed.Reaction.Emoji != "🎉" {
				t.Errorf("folded disposition: Emoji = %q, want 🎉", parsed.Reaction.Emoji)
			}
			if parsed.Reaction.Format != reactionFormatRFC9078 {
				t.Errorf("folded disposition: Format = %q, want %q", parsed.Reaction.Format, reactionFormatRFC9078)
			}
		})
	}
}

// TestContainsFolded pins the folding-aware gate matcher directly,
// including the non-fold cases it must not match.
func TestContainsFolded(t *testing.T) {
	cases := []struct {
		b      string
		needle string
		want   bool
	}{
		{"Content-Disposition: reaction", "Content-Disposition: reaction", true},
		{"Content-Disposition:\r\n reaction", "Content-Disposition: reaction", true},
		{"Content-Disposition: react\r\n ion", "Content-Disposition: reaction", true},
		{"Content-Disposition: react\n ion", "Content-Disposition: reaction", true},
		{"Content-Disposition:\r\n\treaction", "Content-Disposition: reaction", true},
		{"text/vnd.google.email-\r\n reaction+json", "email-reaction+json", true},
		{"text/vnd.google.email-reaction+\r\n json", "email-reaction+json", true},
		{"Content-Disposition: attachment", "Content-Disposition: reaction", false},
		{"Content-Disposition:\r\nattachment", "Content-Disposition: reaction", false},
		{"Content-Disposition: reaction", "email-reaction+json", false},
		// A bare line break is not a fold: the value ends there.
		{"Content-Disposition: react\r\nion", "Content-Disposition: reaction", false},
	}
	for _, c := range cases {
		if got := containsFolded([]byte(c.b), c.needle); got != c.want {
			t.Errorf("containsFolded(%q, %q) = %v, want %v", c.b, c.needle, got, c.want)
		}
	}
}

// TestReauditR26NonEnglishNegatives: the quote-block guard still holds
// for non-English banners — a banner quoted mid-body, or top-posted
// without the quoted original, stays an ordinary email.
func TestReauditR26NonEnglishNegatives(t *testing.T) {
	engine := testEngine()

	// German banner quoted mid-body, quote block present.
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r26n1@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHallo,\r\n\r\n[Daumen hoch] Max Mustermann hat auf Ihre Nachricht reagiert:\r\n\r\n" +
		reauditR26GermanQuoteBlock +
		"\r\nZitierter Text.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m26n1"); parsed.Reaction != nil {
		t.Errorf("German banner mid-body: Reaction = %+v, want nil", parsed.Reaction)
	}

	// German banner first line but no quote block.
	raw = "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r26n2@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n[Daumen hoch] Max Mustermann hat auf Ihre Nachricht reagiert:\r\n\r\nEin bisschen Text, kein zitiertes Original.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m26n2"); parsed.Reaction != nil {
		t.Errorf("German banner without quote block: Reaction = %+v, want nil", parsed.Reaction)
	}

	// French emoji banner first line but no quote block.
	raw = "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r26n3@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n❤️ Marie Dupont a réagi à votre message :\r\n\r\nUn peu de texte, pas d'original cité.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m26n3"); parsed.Reaction != nil {
		t.Errorf("French emoji banner without quote block: Reaction = %+v, want nil", parsed.Reaction)
	}

	// Ordinary German reply with a German quote block but no banner.
	raw = "From: a@example.com\r\nTo: b@example.com\r\nSubject: Re: x\r\nMessage-ID: <r26n4@example.com>\r\nIn-Reply-To: <o@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nDanke, bis Montag.\r\n\r\n" +
		reauditR26GermanQuoteBlock +
		"\r\nZitierter Text.\r\n"
	if parsed := engine.parseMessageBodyInternal([]byte(raw), "m26n4"); parsed.Reaction != nil {
		t.Errorf("ordinary German reply with quote block: Reaction = %+v, want nil", parsed.Reaction)
	}
}
