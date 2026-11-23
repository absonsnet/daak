package reaction

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"

	gomessage "github.com/emersion/go-message"
)

func testOriginal() Original {
	return Original{
		MessageID: "<orig@example.com>",
		Subject:   "Hello there",
		FromName:  "Alice",
		FromEmail: "alice@example.com",
		To:        []string{"me@example.com"},
	}
}

func testOwnEmails() map[string]bool {
	return map[string]bool{"me@example.com": true}
}

// --- MIME structure ------------------------------------------------------

// mimePart is one leaf of the parsed MIME tree: its content type,
// disposition, and decoded body.
type mimePart struct {
	contentType string
	disposition string
	body        string
	children    []*mimePart
}

// parseMIMEParts parses raw RFC 822 bytes into a tree of mimeParts using
// the same go-message library the sync parser uses, so transfer
// encodings are decoded the same way on both sides.
func parseMIMEParts(t *testing.T, raw []byte) *mimePart {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	entity, err := gomessage.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gomessage.Read() error = %v", err)
	}
	_ = msg
	return walkEntity(t, entity)
}

func walkEntity(t *testing.T, entity *gomessage.Entity) *mimePart {
	t.Helper()
	ct, _, _ := mime.ParseMediaType(entity.Header.Get("Content-Type"))
	disp, _, _ := mime.ParseMediaType(entity.Header.Get("Content-Disposition"))
	part := &mimePart{contentType: ct, disposition: disp}
	if mr := entity.MultipartReader(); mr != nil {
		for {
			child, err := mr.NextPart()
			if err != nil {
				break
			}
			part.children = append(part.children, walkEntity(t, child))
		}
		return part
	}
	body, err := io.ReadAll(entity.Body)
	if err != nil {
		t.Fatalf("reading part body: %v", err)
	}
	part.body = string(body)
	return part
}

func TestBuildMessageStructure(t *testing.T) {
	raw, messageID, err := BuildMessage(BuildOptions{
		Emoji:       "👍",
		SenderName:  "Me",
		SenderEmail: "me@example.com",
		Original:    testOriginal(),
		References:  []string{"<thread@example.com>", "<orig@example.com>"},
	})
	if err != nil {
		t.Fatalf("BuildMessage() error = %v", err)
	}
	if messageID == "" || !strings.HasPrefix(messageID, "<") || !strings.HasSuffix(messageID, ">") {
		t.Fatalf("BuildMessage() messageID = %q, want an angle-bracketed id", messageID)
	}

	// Headers.
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	if got := msg.Header.Get("From"); !strings.Contains(got, "me@example.com") {
		t.Errorf("From = %q, want it to contain me@example.com", got)
	}
	if got := msg.Header.Get("To"); got != `"Alice" <alice@example.com>` {
		t.Errorf("To = %q, want %q (original sender only)", got, `"Alice" <alice@example.com>`)
	}
	if got := msg.Header.Get("Subject"); got != "Re: Hello there" {
		t.Errorf("Subject = %q, want %q", got, "Re: Hello there")
	}
	if got := msg.Header.Get("In-Reply-To"); got != "<orig@example.com>" {
		t.Errorf("In-Reply-To = %q, want %q", got, "<orig@example.com>")
	}
	if got := msg.Header.Get("References"); got != "<thread@example.com> <orig@example.com>" {
		t.Errorf("References = %q, want %q", got, "<thread@example.com> <orig@example.com>")
	}
	if got := msg.Header.Get("Message-ID"); got != messageID {
		t.Errorf("Message-ID header = %q, want returned id %q", got, messageID)
	}

	// Body tree: multipart/mixed with [multipart/alternative, reaction].
	root := parseMIMEParts(t, raw)
	if root.contentType != "multipart/mixed" {
		t.Fatalf("outer content type = %q, want multipart/mixed", root.contentType)
	}
	if len(root.children) != 2 {
		t.Fatalf("outer has %d children, want 2 (alternative + reaction part)", len(root.children))
	}
	alt := root.children[0]
	if alt.contentType != "multipart/alternative" {
		t.Fatalf("first child = %q, want multipart/alternative", alt.contentType)
	}
	if len(alt.children) != 3 {
		t.Fatalf("alternative has %d children, want 3 (plain, gmail json, html)", len(alt.children))
	}
	plain, gmail, html := alt.children[0], alt.children[1], alt.children[2]
	if plain.contentType != "text/plain" {
		t.Errorf("alternative[0] = %q, want text/plain", plain.contentType)
	}
	if !strings.Contains(plain.body, "👍 Me reacted to your message:") {
		t.Errorf("plain body = %q, want the banner", plain.body)
	}
	if gmail.contentType != GmailReactionContentType {
		t.Errorf("alternative[1] = %q, want %q (Gmail part between plain and html)", gmail.contentType, GmailReactionContentType)
	}
	if strings.TrimSpace(gmail.body) != `{"version": 1, "emoji": "👍"}` {
		t.Errorf("gmail body = %q, want %q", strings.TrimSpace(gmail.body), `{"version": 1, "emoji": "👍"}`)
	}
	if html.contentType != "text/html" {
		t.Errorf("alternative[2] = %q, want text/html", html.contentType)
	}
	if !strings.Contains(html.body, "👍 Me reacted to your message:") {
		t.Errorf("html body = %q, want the banner", html.body)
	}

	reactionPart := root.children[1]
	if reactionPart.contentType != "text/plain" {
		t.Errorf("reaction part content type = %q, want text/plain", reactionPart.contentType)
	}
	if reactionPart.disposition != "reaction" {
		t.Errorf("reaction part disposition = %q, want reaction", reactionPart.disposition)
	}
	if strings.TrimSpace(reactionPart.body) != "👍" {
		t.Errorf("reaction part body = %q, want just the emoji", strings.TrimSpace(reactionPart.body))
	}
}

func TestBuildMessageSubjectAlreadyRe(t *testing.T) {
	orig := testOriginal()
	orig.Subject = "Re: Hello there"
	raw, _, err := BuildMessage(BuildOptions{Emoji: "❤️", SenderName: "Me", SenderEmail: "me@example.com", Original: orig})
	if err != nil {
		t.Fatalf("BuildMessage() error = %v", err)
	}
	msg, _ := mail.ReadMessage(bytes.NewReader(raw))
	if got := msg.Header.Get("Subject"); got != "Re: Hello there" {
		t.Errorf("Subject = %q, want %q (no doubled Re:)", got, "Re: Hello there")
	}
}

func TestBuildMessageNoSenderNameFallsBackToEmail(t *testing.T) {
	raw, _, err := BuildMessage(BuildOptions{Emoji: "🎉", SenderEmail: "me@example.com", Original: testOriginal()})
	if err != nil {
		t.Fatalf("BuildMessage() error = %v", err)
	}
	root := parseMIMEParts(t, raw)
	plain := root.children[0].children[0]
	if !strings.Contains(plain.body, "🎉 me@example.com reacted to your message:") {
		t.Errorf("plain body = %q, want sender email as the name fallback", plain.body)
	}
}

// --- Banner + quoted original (Outlook fallback format) -----------------

func TestBuildMessageQuotesOriginal(t *testing.T) {
	orig := testOriginal()
	orig.Date = time.Date(2026, time.November, 9, 15, 55, 0, 0, time.UTC)
	orig.Cc = []string{"team@example.com"}
	raw, _, err := BuildMessage(BuildOptions{
		Emoji:            "👍",
		SenderName:       "Me",
		SenderEmail:      "me@example.com",
		Original:         orig,
		OriginalBodyText: "Can we meet at 3 on Monday?\n\nThanks,\nAlice",
		OriginalBodyHTML: "<p>Can we meet at 3 on Monday?</p><p>Thanks,<br>Alice</p>",
	})
	if err != nil {
		t.Fatalf("BuildMessage() error = %v", err)
	}
	root := parseMIMEParts(t, raw)
	plain, gmail, html := root.children[0].children[0], root.children[0].children[1], root.children[0].children[2]
	// The MIME writer uses CRLF; normalize before matching body text.
	plainBody := strings.ReplaceAll(plain.body, "\r\n", "\n")

	// Plain part: banner, then the original's headers, then the full body.
	for _, want := range []string{
		"👍 Me reacted to your message:",
		"From: Alice <alice@example.com>",
		"Sent: Monday, November 9, 2026 3:55 PM",
		"To: me@example.com",
		"Cc: team@example.com",
		"Subject: Hello there",
		"Can we meet at 3 on Monday?",
		"Thanks,\nAlice",
	} {
		if !strings.Contains(plainBody, want) {
			t.Errorf("plain body = %q, want it to contain %q", plain.body, want)
		}
	}
	// HTML part: the same banner + headers, then the original's HTML.
	for _, want := range []string{
		"👍 Me reacted to your message:",
		"From: Alice &lt;alice@example.com&gt;",
		"Sent: Monday, November 9, 2026 3:55 PM",
		"Subject: Hello there",
		"<p>Can we meet at 3 on Monday?</p>",
	} {
		if !strings.Contains(html.body, want) {
			t.Errorf("html body = %q, want it to contain %q", html.body, want)
		}
	}
	// The machine-readable parts are untouched by the fallback restyle.
	if strings.TrimSpace(gmail.body) != `{"version": 1, "emoji": "👍"}` {
		t.Errorf("gmail body = %q, want the JSON part unchanged", strings.TrimSpace(gmail.body))
	}
	reactionPart := root.children[1]
	if reactionPart.disposition != "reaction" || strings.TrimSpace(reactionPart.body) != "👍" {
		t.Errorf("reaction part = %q / %q, want disposition reaction with just the emoji", reactionPart.disposition, reactionPart.body)
	}
}

func TestBuildMessageQuoteFallsBackToHTMLBody(t *testing.T) {
	raw, _, err := BuildMessage(BuildOptions{
		Emoji:            "❤️",
		SenderName:       "Me",
		SenderEmail:      "me@example.com",
		Original:         testOriginal(),
		OriginalBodyHTML: "<p>Hello <b>there</b></p>",
	})
	if err != nil {
		t.Fatalf("BuildMessage() error = %v", err)
	}
	root := parseMIMEParts(t, raw)
	plain := root.children[0].children[0]
	if !strings.Contains(plain.body, "Hello there") {
		t.Errorf("plain body = %q, want the HTML body with tags stripped", plain.body)
	}
	if strings.Contains(plain.body, "<p>") || strings.Contains(plain.body, "<b>") {
		t.Errorf("plain body = %q, want no HTML tags in the plain quote", plain.body)
	}
}

// --- Limits ---------------------------------------------------------------

func TestValidateReactionOK(t *testing.T) {
	if err := ValidateReaction("👍", testOriginal(), testOwnEmails()); err != nil {
		t.Errorf("ValidateReaction() error = %v, want nil for a plain 1:1 message", err)
	}
	// Skin-tone / ZWJ / flag forms are one emoji and pass.
	for _, emoji := range []string{"👍🏽", "❤️", "🇺🇸", "👨‍👩‍👧"} {
		if err := ValidateReaction(emoji, testOriginal(), testOwnEmails()); err != nil {
			t.Errorf("ValidateReaction(%q) error = %v, want nil", emoji, err)
		}
	}
}

func TestValidateReactionLimits(t *testing.T) {
	many := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		many = append(many, fmt.Sprintf("person%d@example.com", i))
	}
	cases := []struct {
		name    string
		emoji   string
		orig    Original
		own     map[string]bool
		wantErr error
	}{
		{"two emoji rejected", "👍👍", testOriginal(), testOwnEmails(), ErrInvalidEmoji},
		{"text rejected", "hello", testOriginal(), testOwnEmails(), ErrInvalidEmoji},
		{"empty emoji rejected", "", testOriginal(), testOwnEmails(), ErrInvalidEmoji},
		{"missing Message-ID", "👍", func() Original { o := testOriginal(); o.MessageID = ""; return o }(), testOwnEmails(), ErrNoMessageID},
		{"S/MIME encrypted", "👍", func() Original { o := testOriginal(); o.SMIMEEncrypted = true; return o }(), testOwnEmails(), ErrEncryptedMessage},
		{"PGP encrypted", "👍", func() Original { o := testOriginal(); o.PGPEncrypted = true; return o }(), testOwnEmails(), ErrEncryptedMessage},
		{"group address in To", "👍", func() Original { o := testOriginal(); o.To = []string{"me@example.com", "all@example.com"}; return o }(), testOwnEmails(), ErrGroupRecipient},
		{"list address in Cc", "👍", func() Original { o := testOriginal(); o.Cc = []string{"dev-list@example.com"}; return o }(), testOwnEmails(), ErrGroupRecipient},
		{"more than 20 recipients", "👍", func() Original { o := testOriginal(); o.To = many; return o }(), testOwnEmails(), ErrTooManyRecipients},
		{"exactly 20 recipients allowed", "👍", func() Original {
			o := testOriginal()
			o.To = append([]string{"me@example.com"}, many[:19]...)
			return o
		}(), testOwnEmails(), nil},
		{"user in Bcc list", "👍", func() Original { o := testOriginal(); o.Bcc = []string{"me@example.com"}; return o }(), testOwnEmails(), ErrBCCRecipient},
		{"user nowhere in To/Cc implies BCC", "👍", func() Original {
			o := testOriginal()
			o.To = []string{"someone@example.com"}
			return o
		}(), testOwnEmails(), ErrBCCRecipient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReaction(tc.emoji, tc.orig, tc.own)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateReaction() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateReaction() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
