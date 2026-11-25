// Package enrich is the shared sync-time enrichment engine for Daak.
//
// It runs locally at sync time, per message, after the existing parse:
// List-Unsubscribe parsing, category classification, code/reference
// extraction, and the attention-eligibility rules that later phases reuse.
// No network, no AI service — a few milliseconds of regex/heuristic work.
//
// Classification is rules, not ML, in strict order:
//  1. sender_category_overrides wins
//  2. hard signals (List-Unsubscribe / Precedence: bulk / automated-style
//     local-part => Automated; sender in contacts who has emailed as a
//     person before => Personal)
//  3. domain/keyword tables per sub-category (data/*.json, reviewable)
//  4. default Personal (misfiling a person as Automated is worse than
//     the reverse)
//
// Extraction is precision over recall: a code/reference is only stored
// when a keyword sits nearby (or the sender is a known OTP/travel/order
// sender) and lookalikes (amounts, dates, years, phone fragments) are
// rejected. When two candidates score nearly the same, nothing is
// extracted rather than guessing.
//
// Extracted values are sensitive (one-time codes): they must never be
// written to sync logs or diagnostics. Log only SafeLogFields — never
// Result.ExtractValue. See redact_test.go.
package enrich

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

//go:embed data/domains.json
var domainsJSON []byte

//go:embed data/keywords.json
var keywordsJSON []byte

// Categories.
const (
	CategoryPersonal  = "personal"
	CategoryAutomated = "automated"
)

// Sub-categories, in match priority order (domain match, then keywords).
var subCategoryOrder = []string{
	"Transactions",
	"Orders",
	"Shipping",
	"Travel",
	"Social",
	"Promotions",
	"Updates",
}

// Extract kinds.
const (
	ExtractCode      = "code"
	ExtractReference = "reference"
	ExtractTracking  = "tracking" // Reserved by the schema; no chip extracts it in Phase A.
)

// Input is everything the classifier/extractor needs for one message.
type Input struct {
	FromEmail string
	FromName  string
	Subject   string
	Snippet   string
	// BodyHead is the first ~2000 chars of the plain-text body. Empty at
	// header-sync time (no body yet); enrichment then covers category and
	// List-Unsubscribe only, and the body pass re-enriches fully.
	BodyHead string
	// ListUnsubscribe is the raw List-Unsubscribe header value ("" if absent).
	ListUnsubscribe string
	// Precedence is the raw Precedence header value ("" if absent).
	Precedence string
	// OverrideCategory is the sender's row in sender_category_overrides.
	OverrideCategory string
	HasOverride      bool
	// KnownPersonalSender is true when the sender is in contacts and has
	// emailed as a person before (contact_emails.send_count > 0).
	KnownPersonalSender bool
	// ForceAutomated preserves a prior pass's Automated verdict when this
	// pass cannot see the signal behind it (the Precedence header, which
	// is not stored). The header pass sets Category from Precedence: bulk;
	// the body pass then keeps Automated even though it has no Precedence
	// to look at. An explicit override to Personal still wins (rule 1).
	ForceAutomated bool
}

// Result is the enrichment row stored on the message.
type Result struct {
	ListUnsubscribe     string
	ListUnsubscribePost bool
	Category            string
	SubCategory         string
	ExtractKind         string // "" | "code" | "reference" | "tracking"
	ExtractValue        string
	ExtractRule         string
}

type domainTable struct {
	Orders       []string `json:"orders"`
	OTPSenders   []string `json:"otpSenders"`
	Promotions   []string `json:"promotions"`
	Shipping     []string `json:"shipping"`
	Social       []string `json:"social"`
	Transactions []string `json:"transactions"`
	Travel       []string `json:"travel"`
}

type keywordTable struct {
	Orders       []string `json:"orders"`
	Promotions   []string `json:"promotions"`
	Shipping     []string `json:"shipping"`
	Social       []string `json:"social"`
	Transactions []string `json:"transactions"`
	Travel       []string `json:"travel"`
	Updates      []string `json:"updates"`
}

var (
	domains  domainTable
	keywords keywordTable
)

func init() {
	if err := json.Unmarshal(domainsJSON, &domains); err != nil {
		panic("enrich: invalid data/domains.json: " + err.Error())
	}
	if err := json.Unmarshal(keywordsJSON, &keywords); err != nil {
		panic("enrich: invalid data/keywords.json: " + err.Error())
	}
}

func domainsFor(sub string) []string {
	switch sub {
	case "Orders":
		return domains.Orders
	case "Promotions":
		return domains.Promotions
	case "Shipping":
		return domains.Shipping
	case "Social":
		return domains.Social
	case "Transactions":
		return domains.Transactions
	case "Travel":
		return domains.Travel
	}
	return nil
}

func keywordsFor(sub string) []string {
	switch sub {
	case "Orders":
		return keywords.Orders
	case "Promotions":
		return keywords.Promotions
	case "Shipping":
		return keywords.Shipping
	case "Social":
		return keywords.Social
	case "Transactions":
		return keywords.Transactions
	case "Travel":
		return keywords.Travel
	case "Updates":
		return keywords.Updates
	}
	return nil
}

func domainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[at+1:]))
}

func localPartOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return strings.ToLower(strings.TrimSpace(email))
	}
	return strings.ToLower(strings.TrimSpace(email[:at]))
}

// domainMatches reports whether email's domain is d or a sub-domain of d.
func domainMatches(email, d string) bool {
	dom := domainOf(email)
	if dom == "" {
		return false
	}
	return dom == d || strings.HasSuffix(dom, "."+d)
}

func domainInList(email string, list []string) bool {
	for _, d := range list {
		if domainMatches(email, d) {
			return true
		}
	}
	return false
}

// automatedLocalParts are sender local-parts that are never a person
// writing mail by hand.
var automatedLocalParts = []string{
	"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply",
	"do_not_reply", "automated", "auto", "mailer-daemon", "postmaster",
	"bounce", "bounces", "notifications", "notification", "alerts",
}

func isAutomatedLocalPart(email string) bool {
	lp := localPartOf(email)
	for _, p := range automatedLocalParts {
		if lp == p || strings.HasPrefix(lp, p+"-") || strings.HasPrefix(lp, p+"_") ||
			strings.HasPrefix(lp, p+".") {
			return true
		}
	}
	// noreply@ / donotreply@ with any suffix, e.g. noreply-orders@.
	return strings.Contains(lp, "noreply") || strings.Contains(lp, "donotreply") ||
		strings.Contains(lp, "no-reply")
}

// ParseListUnsubscribe splits a raw List-Unsubscribe header value into the
// raw value (trimmed) and whether RFC 8058 one-click unsubscribe is
// advertised (a <mailto:...> or <https://...> URI plus a
// List-Unsubscribe-Post: List-Unsubscribe=One-Click marker — the marker
// itself is passed separately by the caller via the post header; here we
// only detect the "<...>" URI form being present, the caller sets the
// boolean from the List-Unsubscribe-Post header).
func ParseListUnsubscribe(raw string) (value string, hasOneClick bool) {
	value = strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	// One-click requires a URI in angle brackets; the List-Unsubscribe-Post
	// header is checked by the caller. The boolean here reports whether the
	// value even carries a usable URI.
	hasOneClick = strings.Contains(value, "<mailto:") || strings.Contains(value, "<http")
	return value, hasOneClick
}

// Enrich runs the full classification + extraction for one message.
func Enrich(in Input) Result {
	res := Result{
		Category:    CategoryPersonal,
		SubCategory: "",
	}
	lu, _ := ParseListUnsubscribe(in.ListUnsubscribe)
	res.ListUnsubscribe = lu

	// 1. Sender override wins.
	if in.HasOverride {
		switch in.OverrideCategory {
		case CategoryAutomated, CategoryPersonal:
			res.Category = in.OverrideCategory
		}
		if res.Category == CategoryAutomated {
			res.SubCategory = classifySubCategory(in)
			res.ExtractKind, res.ExtractValue, res.ExtractRule = extract(in, res.SubCategory)
		}
		return res
	}

	// 2. A known personal sender wins over the hard signals (fourth-audit
	// finding 40): a real person's mail carrying a List-Unsubscribe
	// header (or a bulk Precedence, or an automated-looking local part)
	// used to be locked into the Automated tab forever — the header
	// pass filed it Automated and the body pass was forced to keep
	// that verdict. An explicit sender override (rule 1) still wins
	// over this: the user said Automated, they mean Automated.
	if in.KnownPersonalSender {
		res.Category = CategoryPersonal
		return res
	}

	// 3. Hard signals.
	automated := false
	if lu != "" {
		automated = true
	}
	prec := strings.ToLower(strings.TrimSpace(in.Precedence))
	if prec == "bulk" || prec == "list" || prec == "junk" {
		automated = true
	}
	if isAutomatedLocalPart(in.FromEmail) {
		automated = true
	}
	if in.ForceAutomated {
		automated = true
	}
	if automated {
		res.Category = CategoryAutomated
		res.SubCategory = classifySubCategory(in)
		res.ExtractKind, res.ExtractValue, res.ExtractRule = extract(in, res.SubCategory)
		return res
	}

	// 4. Domain / keyword tables.
	if sub := classifySubCategory(in); sub != "" {
		res.Category = CategoryAutomated
		res.SubCategory = sub
		res.ExtractKind, res.ExtractValue, res.ExtractRule = extract(in, res.SubCategory)
		return res
	}

	// 5. Default Personal.
	return res
}

// classifySubCategory returns the sub-category from the domain/keyword
// tables, or "" when nothing matches.
func classifySubCategory(in Input) string {
	// Domains first: the strongest signal.
	for _, sub := range subCategoryOrder {
		if domainInList(in.FromEmail, domainsFor(sub)) {
			return sub
		}
	}
	// Then keywords over subject + snippet + body head.
	haystack := strings.ToLower(in.Subject + "\n" + in.Snippet + "\n" + in.BodyHead)
	for _, sub := range subCategoryOrder {
		for _, kw := range keywordsFor(sub) {
			if kw != "" && strings.Contains(haystack, kw) {
				return sub
			}
		}
	}
	return ""
}

// IsTransactionSender reports whether the sender is a bank/payment sender.
func IsTransactionSender(email string) bool { return domainInList(email, domains.Transactions) }

// IsTravelSender reports whether the sender is an airline/travel sender.
func IsTravelSender(email string) bool { return domainInList(email, domains.Travel) }

// IsShippingSender reports whether the sender is a courier sender.
func IsShippingSender(email string) bool { return domainInList(email, domains.Shipping) }

// IsOTPSender reports whether the sender is a known one-time-code sender.
func IsOTPSender(email string) bool { return domainInList(email, domains.OTPSenders) }

var digitRunRe = regexp.MustCompile(`[0-9]{4,8}`)

// codeKeywordWeights: keyword (lowercase) -> specificity weight.
var codeKeywordWeights = []struct {
	kw     string
	weight int
}{
	{"otp", 3},
	{"one-time code", 3},
	{"one time code", 3},
	{"one-time password", 3},
	{"one time password", 3},
	{"verification code", 3},
	{"security code", 3},
	{"login code", 3},
	{"activation code", 3},
	{"passcode", 3},
	{"confirmation code", 2},
	{"verification pin", 2},
	{"pin", 2},
	{"code", 2},
}

// findKeyword returns the index of the next occurrence of kw in lower at
// or after from, matched on word boundaries — so "pin" never matches
// inside "shipping" and "otp" never matches inside "hotpot".
func findKeyword(lower, kw string, from int) int {
	for {
		idx := strings.Index(lower[from:], kw)
		if idx < 0 {
			return -1
		}
		pos := from + idx
		beforeOK := pos == 0 || !isAlnum(lower[pos-1])
		afterPos := pos + len(kw)
		afterOK := afterPos >= len(lower) || !isAlnum(lower[afterPos])
		if beforeOK && afterOK {
			return pos
		}
		from = pos + 1
		if from >= len(lower) {
			return -1
		}
	}
}

type candidate struct {
	value  string
	score  int
	rule   string
	source string
}

// extract picks exactly one extracted value: a code first, then a
// reference. Returns empty kind when nothing is confident enough.
func extract(in Input, subCategory string) (kind, value, rule string) {
	if k, v, r := extractCode(in); k != "" {
		return k, v, r
	}
	return extractReference(in, subCategory)
}

// textSources returns the searchable texts in priority order.
func textSources(in Input) []struct {
	name string
	text string
} {
	out := []struct {
		name string
		text string
	}{
		{"subject", in.Subject},
		{"snippet", in.Snippet},
		{"body", in.BodyHead},
	}
	// The snippet usually duplicates the body head; searching both is
	// harmless (same value dedupes) and covers header-only syncs.
	return out
}

func sourceBonus(name string) int {
	switch name {
	case "subject":
		return 2
	case "snippet":
		return 1
	default:
		return 0
	}
}

// extractCode finds a 4-8 digit one-time code near a code keyword, or a
// bare code in the subject from a known OTP sender.
func extractCode(in Input) (string, string, string) {
	best := map[string]*candidate{} // value -> best candidate

	for _, src := range textSources(in) {
		if src.text == "" {
			continue
		}
		lower := strings.ToLower(src.text)
		for _, kw := range codeKeywordWeights {
			start := 0
			for {
				kwStart := findKeyword(lower, kw.kw, start)
				if kwStart < 0 {
					break
				}
				kwEnd := kwStart + len(kw.kw)
				start = kwEnd
				winStart := kwStart - 15
				if winStart < 0 {
					winStart = 0
				}
				winEnd := kwEnd + 50
				if winEnd > len(src.text) {
					winEnd = len(src.text)
				}
				window := src.text[winStart:winEnd]
				for _, loc := range digitRunRe.FindAllStringIndex(window, -1) {
					run := window[loc[0]:loc[1]]
					absStart := winStart + loc[0]
					absEnd := winStart + loc[1]
					if rejectCode(src.text, absStart, absEnd, run) {
						continue
					}
					// Distance from the keyword: closer scores higher.
					dist := 0
					if absStart > kwEnd {
						dist = absStart - kwEnd
					} else if absEnd < kwStart {
						dist = kwStart - absEnd
					}
					score := kw.weight + sourceBonus(src.name)
					if dist <= 5 {
						score += 2
					} else if dist <= 20 {
						score += 1
					}
					rule := "code:keyword:" + strings.ReplaceAll(kw.kw, " ", "-") + ":" + src.name
					if c, ok := best[run]; !ok || score > c.score {
						best[run] = &candidate{value: run, score: score, rule: rule, source: src.name}
					}
				}
			}
		}
	}

	// Known-sender pattern: a bare code in the subject from an OTP sender.
	if IsOTPSender(in.FromEmail) && in.Subject != "" {
		for _, loc := range digitRunRe.FindAllStringIndex(in.Subject, -1) {
			run := in.Subject[loc[0]:loc[1]]
			if rejectCode(in.Subject, loc[0], loc[1], run) {
				continue
			}
			score := 2 + sourceBonus("subject")
			if c, ok := best[run]; !ok || score > c.score {
				best[run] = &candidate{value: run, score: score, rule: "code:known-sender:subject", source: "subject"}
			}
		}
	}

	if len(best) == 0 {
		return "", "", ""
	}
	// Highest score wins; a near-tie means ambiguity — extract nothing.
	var ranked []*candidate
	for _, c := range best {
		ranked = append(ranked, c)
	}
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	if len(ranked) > 1 && ranked[0].score-ranked[1].score <= 1 {
		return "", "", ""
	}
	return ExtractCode, ranked[0].value, ranked[0].rule
}

// rejectCode reports whether a digit run is a lookalike, not a code:
// amounts with currency/decimals, dates, years, phone fragments.
func rejectCode(text string, start, end int, run string) bool {
	// Bounded by non-alphanumerics on both sides (a run inside a longer
	// token, e.g. order "AB123456", is not a bare code).
	if start > 0 {
		if c := text[start-1]; isAlnum(c) {
			return true
		}
	}
	if end < len(text) {
		if c := text[end]; isAlnum(c) {
			return true
		}
	}
	// Years: 4 digits 1900-2099.
	if len(run) == 4 {
		if y, err := strconv.Atoi(run); err == nil && y >= 1900 && y <= 2099 {
			return true
		}
	}
	// Amounts: currency symbol immediately before, or a decimal point
	// with a digit on the other side (12.34 / .50 / Rs 1,234).
	before := ""
	if start >= 3 {
		before = text[start-3 : start]
	} else if start > 0 {
		before = text[:start]
	}
	if strings.ContainsAny(before, "$€£¥") || strings.Contains(before, "Rs") ||
		strings.Contains(before, "PKR") || strings.Contains(before, "USD") {
		return true
	}
	if end < len(text) && text[end] == '.' && end+1 < len(text) && isDigit(text[end+1]) {
		return true
	}
	if start > 0 && text[start-1] == '.' && start-2 >= 0 && isDigit(text[start-2]) {
		return true
	}
	// Dates: digit run joined by / or - to another digit group.
	if start > 0 && (text[start-1] == '/' || text[start-1] == '-') && start-2 >= 0 && isDigit(text[start-2]) {
		return true
	}
	if end < len(text) && (text[end] == '/' || text[end] == '-') && end+1 < len(text) && isDigit(text[end+1]) {
		return true
	}
	// Phone fragments: the run is one group of a spaced/dashed digit
	// sequence, e.g. "0300 1234567" or "+92-300-1234567". Only the
	// immediately adjacent group counts — a phone number elsewhere in
	// the text must not veto a real code.
	if start >= 2 && (text[start-1] == ' ' || text[start-1] == '-') {
		j := start - 2
		for j >= 0 && isDigit(text[j]) {
			j--
		}
		if start-2-j >= 3 {
			return true
		}
	}
	if end < len(text) && (text[end] == ' ' || text[end] == '-') {
		j := end + 1
		for j < len(text) && isDigit(text[j]) {
			j++
		}
		if j-(end+1) >= 3 {
			return true
		}
	}
	// A '+' just before the run also marks a phone number.
	if strings.Contains(text[max(0, start-4):start], "+") {
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlnum(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// referenceKeywords: keyword -> weight.
var referenceKeywords = []struct {
	kw     string
	weight int
}{
	{"booking reference", 3},
	{"booking ref", 3},
	{"pnr", 3},
	{"reference number", 3},
	{"reference no", 3},
	{"confirmation number", 2},
	{"order number", 2},
	{"order no", 2},
	{"reference:", 2},
	{"reservation", 2},
}

var referenceTokenRe = regexp.MustCompile(`[A-Z0-9]{5,12}`)

// extractReference finds a booking/PNR/order reference near a reference
// keyword, or a bare reference token in the subject from a known
// travel/order sender. Tracking numbers are never extracted here.
func extractReference(in Input, subCategory string) (string, string, string) {
	best := map[string]*candidate{}

	for _, src := range textSources(in) {
		if src.text == "" {
			continue
		}
		lower := strings.ToLower(src.text)
		for _, kw := range referenceKeywords {
			start := 0
			for {
				kwStart := findKeyword(lower, kw.kw, start)
				if kwStart < 0 {
					break
				}
				kwEnd := kwStart + len(kw.kw)
				start = kwEnd
				winEnd := kwEnd + 50
				if winEnd > len(src.text) {
					winEnd = len(src.text)
				}
				window := src.text[kwEnd:winEnd]
				for _, loc := range referenceTokenRe.FindAllStringIndex(window, -1) {
					tok := window[loc[0]:loc[1]]
					// Token must be bounded (not part of a longer token).
					if loc[0] > 0 && isAlnum(window[loc[0]-1]) {
						continue
					}
					if loc[1] < len(window) && isAlnum(window[loc[1]]) {
						continue
					}
					if !validReferenceToken(tok, kw.kw) {
						continue
					}
					score := kw.weight + sourceBonus(src.name)
					rule := "reference:keyword:" + strings.ReplaceAll(kw.kw, " ", "-") + ":" + src.name
					if c, ok := best[tok]; !ok || score > c.score {
						best[tok] = &candidate{value: tok, score: score, rule: rule, source: src.name}
					}
				}
			}
		}
	}

	// Known-sender pattern: travel/order sender + bare reference in subject.
	if (IsTravelSender(in.FromEmail) || domainInList(in.FromEmail, domains.Orders)) && in.Subject != "" {
		for _, loc := range referenceTokenRe.FindAllStringIndex(in.Subject, -1) {
			tok := in.Subject[loc[0]:loc[1]]
			if loc[0] > 0 && isAlnum(in.Subject[loc[0]-1]) {
				continue
			}
			if loc[1] < len(in.Subject) && isAlnum(in.Subject[loc[1]]) {
				continue
			}
			if !validReferenceToken(tok, "pnr") {
				continue
			}
			score := 2 + sourceBonus("subject")
			if c, ok := best[tok]; !ok || score > c.score {
				best[tok] = &candidate{value: tok, score: score, rule: "reference:known-sender:subject", source: "subject"}
			}
		}
	}

	if len(best) == 0 {
		return "", "", ""
	}
	var ranked []*candidate
	for _, c := range best {
		ranked = append(ranked, c)
	}
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	if len(ranked) > 1 && ranked[0].score-ranked[1].score <= 1 {
		return "", "", ""
	}
	return ExtractReference, ranked[0].value, ranked[0].rule
}

// validReferenceToken checks a candidate reference token: it must look like
// a booking/PNR/order reference, not a year, a plain word, or a date.
func validReferenceToken(tok, kw string) bool {
	hasLetter, hasDigit := false, false
	for i := 0; i < len(tok); i++ {
		if tok[i] >= 'A' && tok[i] <= 'Z' {
			hasLetter = true
		}
		if tok[i] >= '0' && tok[i] <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter {
		return false // all digits: a code, not a reference
	}
	if len(tok) == 6 {
		return true // PNR shape (letters, digits, or mixed)
	}
	if !hasDigit {
		return false // a plain word, e.g. "NUMBER"
	}
	if len(tok) < 5 {
		return false
	}
	return true
}
