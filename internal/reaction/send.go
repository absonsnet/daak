package reaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/platform"
	"github.com/google/uuid"
)

// GmailReactionContentType is the MIME part Gmail uses for reactions.
const GmailReactionContentType = "text/vnd.google.email-reaction+json"

// MaxRecipients is Gmail's limit: reactions are not supported for messages
// with more than 20 recipients in total (To + Cc + Bcc).
const MaxRecipients = 20

// Limit errors returned by ValidateReaction. Each limit has its own
// sentinel so the UI can surface a distinct message per failure, and so
// tests can distinguish them with errors.Is.
var (
	// ErrInvalidEmoji is returned when the emoji is not exactly one emoji.
	ErrInvalidEmoji = errors.New("reaction emoji must be a single emoji")
	// ErrNoMessageID is returned when the original has no Message-ID to
	// link the reaction to via In-Reply-To.
	ErrNoMessageID = errors.New("original message has no Message-ID, cannot link a reaction to it")
	// ErrGroupRecipient is returned when the original was sent to a group
	// or mailing-list address.
	ErrGroupRecipient = errors.New("reactions are not supported for messages sent to a group or mailing list")
	// ErrTooManyRecipients is returned when the original has more than
	// MaxRecipients recipients in total.
	ErrTooManyRecipients = errors.New("reactions are not supported for messages with more than 20 recipients")
	// ErrBCCRecipient is returned when the current user was BCC'd on the
	// original message.
	ErrBCCRecipient = errors.New("reactions are not supported when you were BCC'd on the original message")
	// ErrEncryptedMessage is returned when the original message is
	// encrypted (PGP or S/MIME).
	ErrEncryptedMessage = errors.New("reactions are not supported for encrypted messages")
)

// Original is the envelope of the message being reacted to, in the form
// the sender needs it. Addresses are plain email strings; the caller
// (app layer) parses them out of the stored message lists.
type Original struct {
	// MessageID is the original's Message-ID header value, angle
	// brackets optional.
	MessageID string
	Subject   string
	// FromName / FromEmail are the original sender; the reaction is
	// addressed to them and to nobody else.
	FromName  string
	FromEmail string
	To        []string
	Cc        []string
	Bcc       []string
	// Date is the original's sent date, for the "Sent:" line of the
	// quoted original in the fallback bodies. Zero omits the line.
	Date time.Time
	// SMIMEEncrypted / PGPEncrypted mirror the message store's flags for
	// the original (see internal/message.Message).
	SMIMEEncrypted bool
	PGPEncrypted   bool
}

// ValidateReaction enforces the Gmail-like limits before sending, plus
// the emoji and linkability checks. ownEmails is the set of the current
// user's addresses on the original's account (account email + identities),
// lowercased. When it is empty (account identities unavailable) the BCC
// check is skipped — there is no address to compare against — while the
// other checks still run. Checks run in a fixed order so a message
// violating several limits always reports the same one.
func ValidateReaction(emoji string, orig Original, ownEmails map[string]bool) error {
	if !IsSingleEmoji(emoji) {
		return ErrInvalidEmoji
	}
	if strings.TrimSpace(orig.MessageID) == "" {
		return ErrNoMessageID
	}
	if orig.SMIMEEncrypted || orig.PGPEncrypted {
		return ErrEncryptedMessage
	}
	for _, addr := range append(append([]string{}, orig.To...), orig.Cc...) {
		if isGroupAddress(addr) {
			return ErrGroupRecipient
		}
	}
	if countDistinctRecipients(orig) > MaxRecipients {
		return ErrTooManyRecipients
	}
	if len(ownEmails) > 0 && isBCCRecipient(orig, ownEmails) {
		return ErrBCCRecipient
	}
	return nil
}

// countDistinctRecipients counts distinct addresses across To + Cc + Bcc
// (case-insensitive, whitespace-trimmed), so the same person listed twice
// is one recipient.
func countDistinctRecipients(orig Original) int {
	seen := make(map[string]bool)
	for _, list := range [][]string{orig.To, orig.Cc, orig.Bcc} {
		for _, addr := range list {
			addr = strings.ToLower(strings.TrimSpace(addr))
			if addr != "" {
				seen[addr] = true
			}
		}
	}
	return len(seen)
}

// isBCCRecipient reports whether the current user was BCC'd on the
// original: their address is in the stored Bcc list, or it appears in
// neither To nor Cc while they are not the sender. The second case is
// the only signal a received message carries — BCC recipients are not
// named in the headers a recipient sees — and it also covers mail that
// reached the user through a list or an undisclosed-recipients header,
// which reactions do not support either.
func isBCCRecipient(orig Original, ownEmails map[string]bool) bool {
	in := func(list []string) bool {
		for _, addr := range list {
			if ownEmails[strings.ToLower(strings.TrimSpace(addr))] {
				return true
			}
		}
		return false
	}
	if in(orig.Bcc) {
		return true
	}
	if in(orig.To) || in(orig.Cc) {
		return false
	}
	// Not addressed to the user at all: BCC'd, unless the user sent the
	// original themselves (reacting to one's own sent mail).
	return !ownEmails[strings.ToLower(strings.TrimSpace(orig.FromEmail))]
}

// isGroupAddress reports whether an address looks like a group or
// mailing-list address. The List-ID / Precedence: list headers that
// identify a list authoritatively are not stored in the DB, so this is
// a pattern match on the envelope address: well-known broadcast local
// parts (all, everyone, announce, …) and list-style affixes (-list,
// -announce, list-, …). A group address that matches none of these
// patterns is not caught — storing List-ID at sync time would sharpen
// this check, but the pattern match covers the common cases without a
// migration.
func isGroupAddress(addr string) bool {
	local := strings.ToLower(strings.TrimSpace(addr))
	if i := strings.Index(local, "@"); i >= 0 {
		local = local[:i]
	}
	switch local {
	case "all", "everyone", "everybody", "announce", "announcements":
		return true
	}
	for _, suffix := range []string{"-list", "-announce", "-all", "-members", "-everyone"} {
		if strings.HasSuffix(local, suffix) {
			return true
		}
	}
	for _, prefix := range []string{"list-", "announce-"} {
		if strings.HasPrefix(local, prefix) {
			return true
		}
	}
	return false
}

// BuildOptions are the inputs to BuildMessage.
type BuildOptions struct {
	Emoji string
	// SenderName / SenderEmail are the reacting user's display name and
	// address (the From identity for the original's account).
	SenderName  string
	SenderEmail string
	Original    Original
	// References is the thread References list (each entry already in
	// angle brackets), parent's References plus the original's
	// Message-ID. Empty falls back to just the original's Message-ID.
	References []string
	// OriginalBodyText / OriginalBodyHTML are the original message's
	// stored body, quoted in full in the human-readable fallback
	// bodies (the Outlook fallback format). At least one should be
	// set — the caller fetches the body first when it has not been
	// fetched yet.
	OriginalBodyText string
	OriginalBodyHTML string
}

// BuildMessage builds the reaction email as raw RFC 822 bytes and returns
// them with the generated Message-ID (angle-bracketed) so the caller can
// record the sent reaction under that id.
//
// MIME structure (chosen so both specs' placement rules hold):
//
//	multipart/mixed
//	├── multipart/alternative
//	│   ├── text/plain            fallback: banner + quoted original (full body)
//	│   ├── text/vnd.google.email-reaction+json   {"version": 1, "emoji": "…"} (between plain and html, per Google)
//	│   └── text/html             fallback: banner + quoted original (full body)
//	└── text/plain; Content-Disposition: reaction   single emoji line (direct child of the outermost multipart, per RFC 9078)
//
// The fallback bodies use the Outlook fallback format: a banner line
// ("👍 <name> reacted to your message:") followed by the original
// message in full (From/Sent/To/Subject headers, then the body).
// Clients that understand the Gmail JSON part render the reaction
// inline on the original; every other client shows this body.
//
// Headers: From is the sender identity, To is the original sender only,
// Subject is "Re: <original subject>", In-Reply-To and References point
// at the original's Message-ID.
func BuildMessage(opts BuildOptions) (raw []byte, messageID string, err error) {
	if err := ValidateReaction(opts.Emoji, opts.Original, nil); err != nil {
		// Builder precondition: emoji, Message-ID, encryption, group,
		// and count limits. The BCC check needs the caller's own
		// addresses (nil here, so skipped) and runs in the app layer
		// via the full ValidateReaction call before BuildMessage.
		return nil, "", err
	}
	orig := opts.Original
	senderName := strings.TrimSpace(opts.SenderName)
	if senderName == "" {
		senderName = strings.TrimSpace(opts.SenderEmail)
	}
	banner := fmt.Sprintf("%s %s reacted to your message:", strings.TrimSpace(opts.Emoji), senderName)

	messageID = fmt.Sprintf("<%s@daak>", uuid.New().String())
	inReplyTo := ensureAngleBrackets(orig.MessageID)
	refs := opts.References
	if len(refs) == 0 {
		refs = []string{inReplyTo}
	}
	raw, err = buildMixedBody(opts, banner, messageID, inReplyTo, refs)
	if err != nil {
		return nil, "", err
	}
	return raw, messageID, nil
}

// fallbackPlainBody renders the human-readable fallback for the
// text/plain part: the banner, a separator, the original's headers,
// then the original's full body text.
func fallbackPlainBody(opts BuildOptions, banner string) string {
	orig := opts.Original
	var b strings.Builder
	b.WriteString(banner)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("_", 60))
	b.WriteString("\n")
	b.WriteString("From: " + formatAddress(orig.FromName, orig.FromEmail) + "\n")
	if !orig.Date.IsZero() {
		b.WriteString("Sent: " + orig.Date.Format("Monday, January 2, 2006 3:04 PM") + "\n")
	}
	if len(orig.To) > 0 {
		b.WriteString("To: " + strings.Join(orig.To, ", ") + "\n")
	}
	if len(orig.Cc) > 0 {
		b.WriteString("Cc: " + strings.Join(orig.Cc, ", ") + "\n")
	}
	b.WriteString("Subject: " + orig.Subject + "\n")
	b.WriteString("\n")
	b.WriteString(plainBodyText(opts))
	return b.String()
}

// fallbackHTMLBody renders the same fallback for the text/html part:
// the banner, a rule, the original's headers, then the original's
// full body (its stored HTML when available, else the plain text).
func fallbackHTMLBody(opts BuildOptions, banner string) string {
	orig := opts.Original
	var b strings.Builder
	b.WriteString("<p>" + escapeHTML(banner) + "</p>")
	b.WriteString("<hr>")
	b.WriteString("<p>")
	b.WriteString("From: " + escapeHTML(formatAddress(orig.FromName, orig.FromEmail)) + "<br>")
	if !orig.Date.IsZero() {
		b.WriteString("Sent: " + escapeHTML(orig.Date.Format("Monday, January 2, 2006 3:04 PM")) + "<br>")
	}
	if len(orig.To) > 0 {
		b.WriteString("To: " + escapeHTML(strings.Join(orig.To, ", ")) + "<br>")
	}
	if len(orig.Cc) > 0 {
		b.WriteString("Cc: " + escapeHTML(strings.Join(orig.Cc, ", ")) + "<br>")
	}
	b.WriteString("Subject: " + escapeHTML(orig.Subject))
	b.WriteString("</p>")
	if strings.TrimSpace(opts.OriginalBodyHTML) != "" {
		b.WriteString(opts.OriginalBodyHTML)
	} else {
		b.WriteString("<p>" + strings.ReplaceAll(escapeHTML(plainBodyText(opts)), "\n", "<br>") + "</p>")
	}
	return b.String()
}

// plainBodyText is the original's full body as plain text: the stored
// plain body when present, else the stored HTML with its tags
// stripped.
func plainBodyText(opts BuildOptions) string {
	if strings.TrimSpace(opts.OriginalBodyText) != "" {
		return opts.OriginalBodyText
	}
	return stripHTMLTags(opts.OriginalBodyHTML)
}

// formatAddress renders a name + address the way a mail header
// shows it: "Name <address>", or just the address when unnamed.
func formatAddress(name, address string) string {
	name = strings.TrimSpace(name)
	address = strings.TrimSpace(address)
	if name == "" {
		return address
	}
	return fmt.Sprintf("%s <%s>", name, address)
}

// stripHTMLTags removes tags from a small HTML fragment and unescapes
// the common entities, for the plain-text fallback when only the
// HTML body was stored. It is a quote helper, not a sanitizer.
func stripHTMLTags(html string) string {
	var b strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	s := b.String()
	for _, pair := range [][2]string{
		{"&nbsp;", " "}, {"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", `"`}, {"&#39;", "'"},
	} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.TrimSpace(s)
}

// buildMixedBody writes the full message (headers + multipart/mixed body).
// Split out of BuildMessage so the boundary is generated exactly once.
func buildMixedBody(opts BuildOptions, banner, messageID, inReplyTo string, refs []string) ([]byte, error) {
	orig := opts.Original
	var buf bytes.Buffer
	writeHeader(&buf, "From", (&mail.Address{Name: opts.SenderName, Address: opts.SenderEmail}).String())
	writeHeader(&buf, "To", (&mail.Address{Name: orig.FromName, Address: strings.TrimSpace(orig.FromEmail)}).String())
	writeHeader(&buf, "Subject", encodeSubject(ReactionSubject(orig.Subject)))
	writeHeader(&buf, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&buf, "Message-ID", messageID)
	writeHeader(&buf, "MIME-Version", "1.0")
	writeHeader(&buf, "User-Agent", platform.AppName+" Email Client")
	writeHeader(&buf, "In-Reply-To", inReplyTo)
	writeHeader(&buf, "References", strings.Join(refs, " "))
	// Outer multipart/mixed: the alternative block first, then the RFC 9078
	// reaction part as a direct child of the outermost multipart.
	mixed := multipart.NewWriter(&buf)
	writeHeader(&buf, "Content-Type", fmt.Sprintf("multipart/mixed; boundary=%q", mixed.Boundary()))
	buf.WriteString("\r\n")

	// multipart/alternative: plain, Gmail JSON, html — in that order.
	altBoundary := uuid.New().String()
	altHeader := textproto.MIMEHeader{}
	altHeader.Set("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", altBoundary))
	altPart, err := mixed.CreatePart(altHeader)
	if err != nil {
		return nil, err
	}
	alt := multipart.NewWriter(altPart)
	if err := alt.SetBoundary(altBoundary); err != nil {
		return nil, err
	}

	textHeader := textproto.MIMEHeader{}
	textHeader.Set("Content-Type", "text/plain; charset=utf-8")
	textHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	textPart, err := alt.CreatePart(textHeader)
	if err != nil {
		return nil, err
	}
	writeQuotedPrintable(textPart, fallbackPlainBody(opts, banner))

	gmailHeader := textproto.MIMEHeader{}
	gmailHeader.Set("Content-Type", GmailReactionContentType+"; charset=utf-8")
	gmailHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	gmailPart, err := alt.CreatePart(gmailHeader)
	if err != nil {
		return nil, err
	}
	writeQuotedPrintable(gmailPart, gmailReactionJSON(opts.Emoji))

	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", "text/html; charset=utf-8")
	htmlHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	htmlPart, err := alt.CreatePart(htmlHeader)
	if err != nil {
		return nil, err
	}
	writeQuotedPrintable(htmlPart, fallbackHTMLBody(opts, banner))

	if err := alt.Close(); err != nil {
		return nil, err
	}

	// RFC 9078 reaction part: direct child of the outermost multipart.
	reactionHeader := textproto.MIMEHeader{}
	reactionHeader.Set("Content-Type", "text/plain; charset=utf-8")
	reactionHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	reactionHeader.Set("Content-Disposition", "reaction")
	reactionPart, err := mixed.CreatePart(reactionHeader)
	if err != nil {
		return nil, err
	}
	writeQuotedPrintable(reactionPart, strings.TrimSpace(opts.Emoji))

	if err := mixed.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gmailReactionJSON renders the Gmail reaction payload byte-for-byte in
// the form the spec shows: {"version": 1, "emoji": "👍"}. The emoji is
// JSON-encoded (it is already validated to be a single emoji, so no
// quoting surprises) rather than interpolated raw.
func gmailReactionJSON(emoji string) string {
	encoded, _ := json.Marshal(emoji)
	return `{"version": 1, "emoji": ` + string(encoded) + `}`
}

// ReactionSubject prefixes "Re: " unless the subject already carries it,
// mirroring the reply subject rule in the composer. Exported so the app
// layer can label the queued reaction row with the same subject the
// built email carries.
func ReactionSubject(subject string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
		return subject
	}
	return "Re: " + subject
}

// ensureAngleBrackets wraps a Message-ID in angle brackets if not already
// present.
func ensureAngleBrackets(msgID string) string {
	msgID = strings.TrimSpace(msgID)
	if !strings.HasPrefix(msgID, "<") {
		msgID = "<" + msgID
	}
	if !strings.HasSuffix(msgID, ">") {
		msgID = msgID + ">"
	}
	return msgID
}

// writeHeader writes a single header line. CRLF characters are stripped
// from the value to prevent header injection.
func writeHeader(w *bytes.Buffer, name, value string) {
	value = strings.NewReplacer("\r\n", "", "\r", "", "\n", "").Replace(value)
	fmt.Fprintf(w, "%s: %s\r\n", name, value)
}

// encodeSubject Q-encodes a subject containing non-ASCII characters.
func encodeSubject(subject string) string {
	for _, r := range subject {
		if r > 127 {
			return mime.QEncoding.Encode("utf-8", subject)
		}
	}
	return subject
}

// writeQuotedPrintable writes content using quoted-printable encoding.
func writeQuotedPrintable(w interface{ Write([]byte) (int, error) }, content string) {
	qpWriter := quotedprintable.NewWriter(w)
	_, _ = qpWriter.Write([]byte(content))
	_ = qpWriter.Close()
}

// escapeHTML escapes special HTML characters.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
