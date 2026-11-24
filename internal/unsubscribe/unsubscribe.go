// Package unsubscribe implements the List-Unsubscribe flow (RFC 2369 /
// RFC 8058) the way Gmail behaves: the headers on the message decide
// everything, never the footer link.
//
//   - List-Unsubscribe-Post: List-Unsubscribe=One-Click plus an https URI
//     means one silent HTTPS POST with body "List-Unsubscribe=One-Click".
//   - An https URI without the one-click marker opens in the browser;
//     the sender's page completes it.
//   - A mailto URI on its own means Daak sends the unsubscribe email
//     itself, with the To and Subject taken from the header.
//
// Safety rules, enforced here so every caller gets them:
//   - The silent POST only ever goes to an https URL. A plain-http
//     one-click degrades to opening in the browser.
//   - The POST never fires except on the user's explicit click; this
//     package only runs when the caller asks it to.
//   - DomainMismatch reports when the unsubscribe host is neither the
//     sender's domain nor a known email-service provider, so the UI can
//     say so plainly instead of hiding it.
package unsubscribe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Method is how an unsubscribe is carried out.
type Method string

const (
	// MethodOneClick is the silent RFC 8058 HTTPS POST.
	MethodOneClick Method = "one_click"
	// MethodBrowser opens the sender's unsubscribe page in the browser.
	MethodBrowser Method = "browser"
	// MethodMailto sends an unsubscribe email from the account.
	MethodMailto Method = "mailto"
	// MethodNone means the headers carry no usable unsubscribe target.
	MethodNone Method = "none"
)

// Target is the parsed unsubscribe instruction for one message.
type Target struct {
	Method Method
	// URL is the https unsubscribe page (MethodBrowser) or the one-click
	// endpoint (MethodOneClick). Empty for mailto/none.
	URL string
	// MailtoAddress and MailtoSubject come from the mailto URI
	// (MethodMailto). The subject defaults to "Unsubscribe" when the
	// header carries none.
	MailtoAddress string
	MailtoSubject string
	// Domain is the host the action goes to (URL host, or the mailto
	// address domain), shown in the button tooltip before the click.
	Domain string
	// DomainMismatch is set by Resolve; see DomainMismatch.
	DomainMismatch bool
}

// angleURI matches one <...> URI inside a List-Unsubscribe value.
var angleURI = regexp.MustCompile(`<([^<>]+)>`)

// Parse turns a raw List-Unsubscribe header value plus the parsed
// List-Unsubscribe-Post marker into the action to take. oneClick is true
// when the Post header contains "List-Unsubscribe=One-Click".
//
// Preference order, matching Gmail: https one-click first, then the first
// https URL (browser), then mailto. A plain-http URI never becomes a
// silent POST target: with the one-click marker it degrades to the
// browser, without it it is ignored in favour of a mailto URI when one
// is present.
func Parse(raw string, oneClick bool) Target {
	var httpsURL, httpURL, mailtoURI string
	for _, m := range angleURI.FindAllStringSubmatch(raw, -1) {
		uri := strings.TrimSpace(m[1])
		lower := strings.ToLower(uri)
		switch {
		case strings.HasPrefix(lower, "https://"):
			if httpsURL == "" {
				httpsURL = uri
			}
		case strings.HasPrefix(lower, "http://"):
			if httpURL == "" {
				httpURL = uri
			}
		case strings.HasPrefix(lower, "mailto:"):
			if mailtoURI == "" {
				mailtoURI = uri
			}
		}
	}

	if oneClick && httpsURL != "" {
		u, err := url.Parse(httpsURL)
		if err == nil {
			return Target{Method: MethodOneClick, URL: httpsURL, Domain: u.Hostname()}
		}
	}
	if httpsURL != "" {
		u, err := url.Parse(httpsURL)
		if err == nil {
			return Target{Method: MethodBrowser, URL: httpsURL, Domain: u.Hostname()}
		}
	}
	if mailtoURI != "" {
		addr, subject := parseMailto(mailtoURI)
		if addr != "" {
			domain := ""
			if at := strings.LastIndex(addr, "@"); at >= 0 {
				domain = addr[at+1:]
			}
			return Target{Method: MethodMailto, MailtoAddress: addr, MailtoSubject: subject, Domain: domain}
		}
	}
	if httpURL != "" {
		// Plain http can never be a silent POST: open it so the user
		// sees where they land instead.
		u, err := url.Parse(httpURL)
		if err == nil {
			return Target{Method: MethodBrowser, URL: httpURL, Domain: u.Hostname()}
		}
	}
	return Target{Method: MethodNone}
}

// parseMailto splits a mailto URI into its address and subject query
// value. The subject defaults to "Unsubscribe" when absent.
func parseMailto(uri string) (address, subject string) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", ""
	}
	address = strings.TrimSpace(u.Opaque)
	if address == "" {
		address = strings.TrimSpace(u.Path)
	}
	// A stray "mailto:" prefix or query string left in the opaque part.
	if i := strings.Index(address, "?"); i >= 0 {
		address = address[:i]
	}
	if address == "" {
		return "", ""
	}
	subject = stripCRLF(u.Query().Get("subject"))
	if strings.TrimSpace(subject) == "" {
		subject = "Unsubscribe"
	}
	return address, subject
}

// stripCRLF removes carriage returns and line feeds from s. Header
// values decoded from the untrusted List-Unsubscribe header (e.g. a
// percent-encoded %0d%0a in a mailto subject) must never reach a raw
// header line, where they would inject arbitrary headers into mail
// sent from the user's account.
func stripCRLF(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// knownESPHosts are email-service-provider domains an unsubscribe link
// may legitimately point at even when they differ from the sender
// domain. A host counts when it equals one of these or is a subdomain
// of one.
var knownESPHosts = []string{
	"sendgrid.net",
	"mailchimp.com",
	"list-manage.com",
	"campaign-archive.com",
	"constantcontact.com",
	"mailerlite.com",
	"sendinblue.com",
	"brevo.com",
	"hubspot.com",
	"hubspotemail.net",
	"exacttarget.com",
	"sfmc-content.com",
	"klaviyo.com",
	"convertkit.com",
	"substack.com",
	"buttondown.email",
	"beehiiv.com",
	"getresponse.com",
	"aweber.com",
	"activecampaign.com",
	"campaignmonitor.com",
	"emarsys.com",
	"braze.com",
	"customer.io",
	"postmarkapp.com",
	"mandrillapp.com",
	"sparkpost.com",
	"mailjet.com",
	"mailgun.org",
	"sendpulse.com",
	"omnisend.com",
	"mailchimpapp.net",
	"sgmail.org",
}

// DomainMismatch reports whether the unsubscribe host looks unrelated to
// the sender: it is neither the sender domain nor a subdomain of it,
// and neither the host nor a subdomain of a known email-service
// provider. An empty host or sender domain never mismatches — there is
// nothing to compare.
func DomainMismatch(senderEmail, unsubHost string) bool {
	senderDomain := ""
	if at := strings.LastIndex(senderEmail, "@"); at >= 0 {
		senderDomain = strings.ToLower(strings.TrimSpace(senderEmail[at+1:]))
	}
	host := strings.ToLower(strings.TrimSpace(unsubHost))
	if senderDomain == "" || host == "" {
		return false
	}
	if host == senderDomain || strings.HasSuffix(host, "."+senderDomain) {
		return false
	}
	for _, esp := range knownESPHosts {
		if host == esp || strings.HasSuffix(host, "."+esp) {
			return false
		}
	}
	return true
}

// Resolve parses the headers and fills in DomainMismatch against the
// sender in one call.
func Resolve(raw string, oneClick bool, senderEmail string) Target {
	t := Parse(raw, oneClick)
	t.DomainMismatch = DomainMismatch(senderEmail, t.Domain)
	return t
}

// PostOneClick fires the RFC 8058 one-click POST: body
// "List-Unsubscribe=One-Click" to an https URL. http URLs are refused
// outright — the caller must degrade those to the browser. A non-2xx
// response is an error. Redirects follow oneClickCheckRedirect: only
// 307/308 re-POSTs to an https URL are followed; 301/302/303 (which
// would turn the POST into a GET the endpoint never sees) and any
// redirect off https are errors, so success is never reported when the
// endpoint never received the POST. The client is injected so tests
// can point it at a local TLS server; nil uses a 15-second default
// client.
func PostOneClick(ctx context.Context, targetURL string, client *http.Client) error {
	u, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid unsubscribe URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("one-click unsubscribe requires https, got %q", u.Scheme)
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// Clone so the caller's client (and its redirect policy) is
	// untouched; the one-click redirect policy below is the security
	// boundary for this POST.
	effective := *client
	effective.CheckRedirect = oneClickCheckRedirect
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL,
		strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return fmt.Errorf("failed to build unsubscribe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := effective.Do(req)
	if err != nil {
		return fmt.Errorf("unsubscribe request failed: %w", err)
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unsubscribe endpoint returned %s", resp.Status)
	}
	return nil
}

// oneClickCheckRedirect is the redirect policy for the RFC 8058
// one-click POST. The default client follows up to 10 redirects and
// converts POST to GET on 301/302/303, dropping the body — the
// endpoint would never see the unsubscribe, yet any 2xx from the
// redirect target would be reported as success. So:
//
//   - 301/302/303 are refused outright (they would turn the POST into
//     a GET);
//   - 307/308 are followed only while they stay on https, re-sending
//     the POST body;
//   - any redirect off https, including an https→http downgrade, is
//     refused.
//
// A refused redirect is an error from PostOneClick, so the unsubscribe
// is never recorded as done when the endpoint never received the POST.
func oneClickCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("unsubscribe redirect: stopped after 10 redirects")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("unsubscribe redirect: refusing redirect to non-https URL %q", req.URL.String())
	}
	// Go builds the redirect request with the converted method before
	// calling CheckRedirect, so a POST that became a GET shows up
	// here as req.Method GET with a POST in via.
	if len(via) > 0 && via[len(via)-1].Method == http.MethodPost && req.Method != http.MethodPost {
		return errors.New("unsubscribe redirect: refusing POST-to-GET redirect (301/302/303); the endpoint would never receive the POST")
	}
	return nil
}

// BuildMailtoMessage builds the raw unsubscribe email for a mailto
// target: plain text, From the account identity, To and Subject taken
// from the header. It returns the raw bytes and the generated
// Message-ID (angle-bracketed) so the caller can record the sent mail.
func BuildMailtoMessage(fromName, fromEmail, to, subject string) ([]byte, string, error) {
	// The subject and display name come from the untrusted
	// List-Unsubscribe header: strip any CR/LF before either reaches
	// a raw header line, where it would inject arbitrary headers
	// (e.g. Bcc:) into mail sent from the user's account.
	subject = stripCRLF(subject)
	fromName = stripCRLF(fromName)
	// Defensive: the caller passes the bare address from Parse, but a
	// stray "?subject=..." suffix must never reach the To header.
	if i := strings.Index(to, "?"); i >= 0 {
		to = to[:i]
	}
	toAddr, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return nil, "", fmt.Errorf("invalid unsubscribe address %q: %w", to, err)
	}
	from := fromEmail
	if strings.TrimSpace(fromName) != "" {
		from = fmt.Sprintf("%s <%s>", quoteName(fromName), fromEmail)
	}
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, "", fmt.Errorf("invalid sender address %q: %w", fromEmail, err)
	}
	messageID := fmt.Sprintf("<%s@daak>", uuid.New().String())
	var buf bytes.Buffer
	// Write the validated plain address strings: Address.String()
	// re-renders nameless addresses as "<a@b>" and quotes plain names,
	// which is valid but noisy in the headers the user may see.
	fromHeader := fromAddr.Address
	if strings.TrimSpace(fromName) != "" {
		fromHeader = fmt.Sprintf("%s <%s>", quoteName(fromName), fromAddr.Address)
	}
	buf.WriteString(fmt.Sprintf("From: %s\r\n", fromHeader))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", toAddr.Address))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	buf.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z)))
	buf.WriteString(fmt.Sprintf("Message-ID: %s\r\n", messageID))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 7bit\r\n")
	buf.WriteString("\r\n")
	buf.WriteString("Please unsubscribe this address from the mailing list.\r\n")
	return buf.Bytes(), messageID, nil
}

// quoteName quotes a display name for a From header when it contains
// anything outside a plain phrase of atoms (a comma, quote, colon,
// and the like); plain names pass through unchanged. CR/LF are
// stripped first so a crafted name can never break out of the header.
func quoteName(name string) string {
	name = stripCRLF(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	for _, r := range name {
		if r == ' ' {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("!#$%&'*+-/=?^_`{|}~", r) {
			continue
		}
		return `"` + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), `"`, `\"`) + `"`
	}
	return name
}
