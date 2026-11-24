package sync

import (
	"bytes"
	"encoding/json"
	"github.com/absonsnet/daak/internal/reaction"
	gomessage "github.com/emersion/go-message"
	"io"
	"mime"
	"strings"
)

// Reaction wire formats detected during body parsing. A reaction is a
// separate email pointing back at the original via In-Reply-To; detection
// here only validates the reaction payload itself. Linking to the original
// and hiding the reaction email happen in fetch.go (handleReaction).
const (
	// gmailReactionContentType is the MIME part Gmail uses for reactions.
	gmailReactionContentType = "text/vnd.google.email-reaction+json"

	// reactionFormatGmail is the DetectedReaction.Format for Gmail's format.
	reactionFormatGmail = "gmail"
	// reactionFormatRFC9078 is the DetectedReaction.Format for RFC 9078.
	reactionFormatRFC9078 = "rfc9078"
	// reactionFormatOutlook is the DetectedReaction.Format for Outlook's
	// fallback reaction email (banner + quoted original, no machine-readable
	// reaction part).
	reactionFormatOutlook = "outlook"
)

// outlookReactionEmojis maps Outlook's bracketed reaction names (its six
// fixed reactions) to the emoji they stand for.
var outlookReactionEmojis = map[string]string{
	"thumbs up": "👍",
	"celebrate": "🎉",
	"heart":     "❤️",
	"laugh":     "😂",
	"surprise":  "😮",
	"sad":       "😢",
}

// maxReactionPartSize bounds how much of a candidate reaction part is read.
// A reaction payload is a few dozen bytes; anything larger is malformed.
const maxReactionPartSize = 4096

// DetectedReaction is a validated email reaction found while parsing a
// message body. Format is "gmail" or "rfc9078". Emoji is exactly one
// Unicode emoji.
type DetectedReaction struct {
	Emoji  string
	Format string
}

// detectReaction inspects raw message bytes for a reaction in either
// supported format. Gmail is checked first; a malformed reaction part is
// skipped rather than vetoing a valid one elsewhere in the tree, so a
// message with only malformed reaction parts is not a reaction at all.
// Returns nil when nothing valid is found.
//
// Detection re-parses raw instead of reusing the entity the body walk
// already consumed, and is gated on two marker strings so ordinary
// messages pay only a byte scan: Gmail always emits its content type
// verbatim and RFC 9078 senders emit "Content-Disposition: reaction".
// The gate is case-insensitive (header names and values are case-insensitive
// per RFC, so a sender that re-cases them must not be missed). A sender
// that folds those headers is missed and the message falls back to a
// regular email — the safe default.
//
// The tree is walked exactly once: a MIME part's body can only be read
// once, so separate Gmail and RFC 9078 passes would starve each other.
func detectReaction(raw []byte) *DetectedReaction {
	if !containsFold(raw, "email-reaction+json") &&
		!containsFold(raw, "Content-Disposition: reaction") {
		return nil
	}
	entity, err := gomessage.Read(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return searchReactionParts(entity, 0)
}

// containsFold reports whether needle occurs in b, matching ASCII letters
// case-insensitively. Allocation-free, so the detection gate stays a
// cheap byte scan even for large messages.
func containsFold(b []byte, needle string) bool {
	nb := []byte(needle)
	if len(nb) == 0 {
		return true
	}
	for i := 0; i+len(nb) <= len(b); i++ {
		if bytes.EqualFold(b[i:i+len(nb)], nb) {
			return true
		}
	}
	return false
}

// detectOutlookFallbackReaction checks a plain-text body for Outlook's
// fallback reaction email: a first line of the form "[reaction] Name
// reacted to your message:" (or a raw emoji in place of the bracketed
// name) followed by Outlook's quote block (From:/Sent:/To:/Subject:).
// Returns nil when the shape doesn't match.
//
// The quote-block requirement is the anti-false-positive guard: an
// ordinary reply merely quoting such a line mid-body, or a top-posted
// line without the quoted original, is not a reaction. The reactor's
// name is not parsed — the reaction row takes it from the message
// headers, as for the other formats — only the emoji is extracted here.
func detectOutlookFallbackReaction(bodyText string) *DetectedReaction {
	lines := strings.Split(bodyText, "\n")
	firstIdx := -1
	var first string
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			firstIdx = i
			break
		}
	}
	if firstIdx < 0 {
		return nil
	}
	emoji, ok := parseOutlookBannerLine(first)
	if !ok {
		return nil
	}
	if !hasOutlookQuoteBlock(lines[firstIdx+1:]) {
		return nil
	}
	return &DetectedReaction{Emoji: emoji, Format: reactionFormatOutlook}
}

// parseOutlookBannerLine extracts the emoji from an Outlook fallback
// banner line. Bracketed form: "[thumbs up] Dr Sana Zulfiqar reacted to
// your message:" — the bracketed name maps through outlookReactionEmojis.
// Emoji form: "👍 Dr Sana Zulfiqar reacted to your message:" — the first
// token must be exactly one emoji and a reactor name must follow it.
func parseOutlookBannerLine(line string) (string, bool) {
	const suffix = " reacted to your message:"
	if !strings.HasSuffix(line, suffix) {
		return "", false
	}
	head := strings.TrimSpace(strings.TrimSuffix(line, suffix))
	if head == "" {
		return "", false
	}
	if strings.HasPrefix(head, "[") {
		end := strings.Index(head, "]")
		if end < 0 {
			return "", false
		}
		name := strings.ToLower(strings.TrimSpace(head[1:end]))
		emoji, ok := outlookReactionEmojis[name]
		if !ok {
			return "", false
		}
		if strings.TrimSpace(head[end+1:]) == "" {
			return "", false
		}
		return emoji, true
	}
	fields := strings.Fields(head)
	if len(fields) < 2 {
		return "", false
	}
	if !isSingleEmoji(fields[0]) {
		return "", false
	}
	return fields[0], true
}

// outlookFallbackText returns the text to check for an Outlook fallback
// banner: the plain-text body, or the HTML body stripped of tags when
// there is no plain-text part. Block boundaries become line breaks first,
// so the banner line and the quote block survive stripping as separate
// lines instead of one run-on line.
func outlookFallbackText(result *ParsedBody) string {
	if strings.TrimSpace(result.BodyText) != "" {
		return result.BodyText
	}
	html := result.BodyHTML
	for _, tag := range []string{"</p>", "<br>", "<br/>", "<br />", "</div>", "</tr>", "</li>"} {
		html = strings.ReplaceAll(html, tag, "\n")
	}
	return stripHTMLTags(html)
}

// hasOutlookQuoteBlock reports whether the lines contain Outlook's reply
// quote header block — From:, Sent:, To: and Subject:, in that order —
// within the first few lines. The block immediately follows the banner in
// a fallback reaction email.
func hasOutlookQuoteBlock(lines []string) bool {
	want := []string{"from:", "sent:", "to:", "subject:"}
	next := 0
	checked := 0
	for _, l := range lines {
		if checked >= 12 {
			break
		}
		checked++
		trimmed := strings.ToLower(strings.TrimSpace(l))
		if strings.HasPrefix(trimmed, want[next]) {
			next++
			if next == len(want) {
				return true
			}
		}
	}
	return false
}

// searchReactionParts walks the MIME tree once, checking every part for a
// Gmail reaction part (valid at any depth) and — at the outermost level
// only, per RFC 9078 — for a reaction part. depth 0 is the message itself,
// depth 1 its direct children. Returns the first valid reaction found.
func searchReactionParts(entity *gomessage.Entity, depth int) *DetectedReaction {
	if emoji, ok := gmailReactionPartEmoji(entity); ok {
		return &DetectedReaction{Emoji: emoji, Format: reactionFormatGmail}
	}
	if depth <= 1 {
		if emoji, ok := rfc9078ReactionPartEmoji(entity); ok {
			return &DetectedReaction{Emoji: emoji, Format: reactionFormatRFC9078}
		}
	}
	mr := entity.MultipartReader()
	if mr == nil {
		return nil
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil
		}
		if found := searchReactionParts(part, depth+1); found != nil {
			return found
		}
	}
}

// gmailReactionPartEmoji validates a Gmail reaction part:
// text/vnd.google.email-reaction+json with a {"version": 1, "emoji": ...}
// payload. Any other version, a missing or multi-emoji value, or
// unparseable JSON is malformed — not a reaction. Returns false for
// parts that are not Gmail reaction parts at all.
func gmailReactionPartEmoji(entity *gomessage.Entity) (string, bool) {
	contentType, params, _ := mime.ParseMediaType(entity.Header.Get("Content-Type"))
	if contentType != gmailReactionContentType {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(entity.Body, maxReactionPartSize))
	if err != nil {
		return "", false
	}
	return parseGmailReactionJSON(decodeCharset(body, params["charset"]))
}

// gmailReactionJSON is the payload of Gmail's reaction part.
type gmailReactionJSON struct {
	Version int    `json:"version"`
	Emoji   string `json:"emoji"`
}

// parseGmailReactionJSON validates a Gmail reaction payload. Version must be
// exactly 1 and emoji exactly one Unicode emoji.
func parseGmailReactionJSON(body string) (string, bool) {
	var payload gmailReactionJSON
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", false
	}
	if payload.Version != 1 {
		return "", false
	}
	if !isSingleEmoji(payload.Emoji) {
		return "", false
	}
	return payload.Emoji, true
}

// rfc9078ReactionPartEmoji returns the emoji if the entity is an RFC 9078
// reaction part — Content-Disposition: reaction with Content-Type
// text/plain whose content is a single line holding one emoji — and false
// otherwise (not a reaction part, or malformed content).
func rfc9078ReactionPartEmoji(entity *gomessage.Entity) (string, bool) {
	disposition, _, _ := mime.ParseMediaType(entity.Header.Get("Content-Disposition"))
	if disposition != "reaction" {
		return "", false
	}
	contentType, params, _ := mime.ParseMediaType(entity.Header.Get("Content-Type"))
	if contentType != "text/plain" {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(entity.Body, maxReactionPartSize))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(decodeCharset(body, params["charset"]))
	if strings.ContainsAny(line, "\r\n") {
		return "", false
	}
	if !isSingleEmoji(line) {
		return "", false
	}
	return line, true
}

// isSingleEmoji delegates to the shared validator in internal/reaction so
// the receive path and the send path agree on what counts as one emoji.
func isSingleEmoji(s string) bool {
	return reaction.IsSingleEmoji(s)
}
