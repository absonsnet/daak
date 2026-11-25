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

// outlookReactionEmojisLocalized maps non-English names for Outlook's six
// fixed reactions to the emoji they stand for. These are the reaction
// names as localised in Outlook's UI (German and French); they are
// best-effort localisations, not captured fallback emails — the generic
// banner path does not depend on the verb wording, only on the bracketed
// token plus the banner shape and the quote block that must follow. No
// attempt is made to enumerate every language: a bracketed token outside
// these maps is not accepted (there would be no emoji to map it to), and
// the raw-emoji banner form is the language-independent path for any
// other language.
var outlookReactionEmojisLocalized = map[string]string{
	// German.
	"daumen hoch": "👍",
	"gefällt mir": "👍",
	"herz":        "❤️",
	"feiern":      "🎉",
	"lachen":      "😂",
	"überrascht":  "😮",
	"traurig":     "😢",
	// French.
	"j'aime":     "👍",
	"pouce levé": "👍",
	"cœur":       "❤️",
	"coeur":      "❤️",
	"j'adore":    "❤️",
	"célébrer":   "🎉",
	"fêter":      "🎉",
	"rire":       "😂",
	"surpris":    "😮",
	"triste":     "😢",
}

// outlookReactionEmoji resolves a bracketed reaction name (case- and
// apostrophe-insensitive) through the English map first, then the
// localised map. It reports false for an unknown name.
func outlookReactionEmoji(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "’", "'")
	if emoji, ok := outlookReactionEmojis[name]; ok {
		return emoji, true
	}
	if emoji, ok := outlookReactionEmojisLocalized[name]; ok {
		return emoji, true
	}
	return "", false
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
// per RFC, so a sender that re-cases them must not be missed) and
// folding-aware: a sender that folds a marker header across two lines
// (CRLF followed by whitespace) is still caught by containsFolded, which
// treats a fold as a space or as nothing, so both a fold at a word
// boundary and a fold inside a word match. A folded marker that reaches
// the MIME walk is handled there too: go-message unfolds folded headers
// by joining the segments with a space, so the part checks strip spaces
// from the media-type/disposition token (see joinFoldedHeaderToken).
//
// The tree is walked exactly once: a MIME part's body can only be read
// once, so separate Gmail and RFC 9078 passes would starve each other.
func detectReaction(raw []byte) *DetectedReaction {
	if !containsFold(raw, "email-reaction+json") &&
		!containsFold(raw, "Content-Disposition: reaction") &&
		!containsFolded(raw, "email-reaction+json") &&
		!containsFolded(raw, "Content-Disposition: reaction") {
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

// containsFolded reports whether needle occurs in b when header folding
// is ignored: a line break (CRLF or LF) followed by a space or tab is a
// fold, and a fold may stand for a space in the needle or for nothing at
// all, so both a fold at a word boundary ("Content-Disposition:\r\n
// reaction") and a fold inside a word ("react\r\n ion") match. A line
// break not followed by whitespace is not a fold and ends the match.
// Allocation-free like containsFold; it only runs when the plain scan
// misses, so ordinary messages pay for it only as a second scan.
func containsFolded(b []byte, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i < len(b); i++ {
		if matchFoldedAt(b, i, needle) {
			return true
		}
	}
	return false
}

// matchFoldedAt reports whether needle matches b starting at start,
// treating folds as described on containsFolded.
func matchFoldedAt(b []byte, start int, needle string) bool {
	k := start
	for j := 0; j < len(needle); {
		if k >= len(b) {
			return false
		}
		if b[k] == '\r' || b[k] == '\n' {
			nk := k
			if b[nk] == '\r' && nk+1 < len(b) && b[nk+1] == '\n' {
				nk++
			}
			nk++
			if nk >= len(b) || (b[nk] != ' ' && b[nk] != '\t') {
				return false
			}
			for nk < len(b) && (b[nk] == ' ' || b[nk] == '\t') {
				nk++
			}
			// The fold stands for a single space in the needle, or for
			// nothing when it falls inside a word.
			if needle[j] == ' ' {
				j++
			}
			k = nk
			continue
		}
		if !asciiFoldEqual(b[k], needle[j]) {
			return false
		}
		k++
		j++
	}
	return true
}

// asciiFoldEqual reports whether two bytes are equal ignoring ASCII case.
func asciiFoldEqual(a, b byte) bool {
	if a == b {
		return true
	}
	if 'A' <= a && a <= 'Z' {
		a += 'a' - 'A'
	}
	if 'A' <= b && b <= 'Z' {
		b += 'a' - 'A'
	}
	return a == b
}

// joinFoldedHeaderToken removes spaces and tabs from the token before the
// first ";" in a header value. go-message unfolds a folded header by
// joining its segments with a space, so a fold inside a token surfaces as
// an embedded space ("react ion", "text/vnd.google.email-
// reaction+json") that mime.ParseMediaType would not match. Media-type
// and disposition tokens never contain a space legitimately, so stripping
// spaces from the token is safe; the parameters after ";" are untouched.
func joinFoldedHeaderToken(v string) string {
	head, rest, ok := strings.Cut(v, ";")
	head = strings.ReplaceAll(head, " ", "")
	head = strings.ReplaceAll(head, "\t", "")
	if !ok {
		return head
	}
	return head + ";" + rest
}

// detectOutlookFallbackReaction checks a plain-text body for Outlook's
// fallback reaction email: a first line of the form "[reaction] Name
// reacted to your message:" (or a raw emoji in place of the bracketed
// name) followed by Outlook's quote block (From:/Sent:/To:/Subject:).
// Returns nil when the shape doesn't match.
//
// The banner is matched two ways: the strict English wording first, then
// a language-independent shape — bracketed known reaction name (English
// or localised) or a single emoji, plus a reactor name, plus any words,
// ending in a colon — so non-English Outlook banners are recognised
// without requiring the English words. Either way, the quote-block
// requirement is the anti-false-positive guard: an ordinary reply merely
// quoting such a line mid-body, or a top-posted line without the quoted
// original, is not a reaction. The reactor's name is not parsed — the
// reaction row takes it from the message headers, as for the other
// formats — only the emoji is extracted here.
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
		emoji, ok = parseOutlookBannerLineGeneric(first)
	}
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
		emoji, ok := outlookReactionEmoji(head[1:end])
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

// parseOutlookBannerLineGeneric extracts the emoji from a non-English
// Outlook fallback banner line, by shape rather than wording: either
// "[<reaction>] <reactor name> <any words>:" or "<single emoji> <reactor
// name> <any words>:" — the bracketed/emoji reaction marker, a reactor
// name, at least one more word, and a trailing colon. The verb wording is
// not checked, so any language matches; the bracketed token must still
// resolve through the known reaction names (English or localised), since
// an unmapped token has no emoji to report. This parser alone never
// decides a reaction: detectOutlookFallbackReaction only accepts its
// result when the quote block follows, which is what keeps ordinary
// replies that happen to contain such a line from matching.
func parseOutlookBannerLineGeneric(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasSuffix(line, ":") {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimSuffix(line, ":"))
	if body == "" {
		return "", false
	}
	if strings.HasPrefix(body, "[") {
		end := strings.Index(body, "]")
		if end < 0 {
			return "", false
		}
		token := body[1:end]
		if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "[]:") {
			return "", false
		}
		if len([]rune(token)) > 40 {
			return "", false
		}
		emoji, ok := outlookReactionEmoji(token)
		if !ok {
			return "", false
		}
		// Reactor name plus at least one more word.
		if len(strings.Fields(strings.TrimSpace(body[end+1:]))) < 2 {
			return "", false
		}
		return emoji, true
	}
	fields := strings.Fields(body)
	if len(fields) < 3 {
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
// a fallback reaction email. Non-English Outlook localises the labels
// (German Von:/Gesendet:/An:/Betreff:, French De:/Envoyé:/À:/Objet:),
// so each language's set is tried; a block still needs all four labels
// of one set, in order, so the guard is as strong as before. French
// labels carry a space before the colon ("De :"), which is normalised
// away before matching.
func hasOutlookQuoteBlock(lines []string) bool {
	labelSets := [][]string{
		{"from:", "sent:", "to:", "subject:"},
		{"von:", "gesendet:", "an:", "betreff:"},
		{"de:", "envoyé:", "à:", "objet:"},
	}
	for _, want := range labelSets {
		next := 0
		checked := 0
		for _, l := range lines {
			if checked >= 12 {
				break
			}
			checked++
			trimmed := strings.ToLower(strings.TrimSpace(l))
			trimmed = strings.ReplaceAll(trimmed, " :", ":")
			if strings.HasPrefix(trimmed, want[next]) {
				next++
				if next == len(want) {
					return true
				}
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
	contentType, params, _ := mime.ParseMediaType(joinFoldedHeaderToken(entity.Header.Get("Content-Type")))
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
	disposition, _, _ := mime.ParseMediaType(joinFoldedHeaderToken(entity.Header.Get("Content-Disposition")))
	if disposition != "reaction" {
		return "", false
	}
	contentType, params, _ := mime.ParseMediaType(joinFoldedHeaderToken(entity.Header.Get("Content-Type")))
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
