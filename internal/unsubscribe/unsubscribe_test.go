package unsubscribe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

func TestParseOneClick(t *testing.T) {
	got := Parse("<https://news.example.com/unsub?id=123>, <mailto:unsub@example.com?subject=unsub-123>", true)
	if got.Method != MethodOneClick {
		t.Fatalf("Method = %q, want one_click", got.Method)
	}
	if got.URL != "https://news.example.com/unsub?id=123" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Domain != "news.example.com" {
		t.Fatalf("Domain = %q", got.Domain)
	}
}

func TestParseHTTPSPlain(t *testing.T) {
	got := Parse("<https://news.example.com/unsub?id=123>", false)
	if got.Method != MethodBrowser {
		t.Fatalf("Method = %q, want browser", got.Method)
	}
	if got.URL != "https://news.example.com/unsub?id=123" {
		t.Fatalf("URL = %q", got.URL)
	}
}

func TestParseMailtoOnly(t *testing.T) {
	got := Parse("<mailto:unsub@example.com?subject=unsub-123>", false)
	if got.Method != MethodMailto {
		t.Fatalf("Method = %q, want mailto", got.Method)
	}
	if got.MailtoAddress != "unsub@example.com" {
		t.Fatalf("MailtoAddress = %q", got.MailtoAddress)
	}
	if got.MailtoSubject != "unsub-123" {
		t.Fatalf("MailtoSubject = %q", got.MailtoSubject)
	}
	if got.Domain != "example.com" {
		t.Fatalf("Domain = %q", got.Domain)
	}
}

func TestParseMailtoDefaultSubject(t *testing.T) {
	got := Parse("<mailto:leave@example.com>", false)
	if got.Method != MethodMailto || got.MailtoSubject != "Unsubscribe" {
		t.Fatalf("got %+v, want mailto with default subject", got)
	}
}

func TestParseOneClickMarkerWithoutHTTPSFallsBackToMailto(t *testing.T) {
	// One-click marker but only a mailto URI: no silent POST is possible.
	got := Parse("<mailto:unsub@example.com?subject=x>", true)
	if got.Method != MethodMailto {
		t.Fatalf("Method = %q, want mailto", got.Method)
	}
}

func TestParseHTTPDegradesToBrowser(t *testing.T) {
	// Plain http with the one-click marker must NOT become a POST.
	got := Parse("<http://news.example.com/unsub?id=1>", true)
	if got.Method != MethodBrowser {
		t.Fatalf("Method = %q, want browser", got.Method)
	}
	if got.URL != "http://news.example.com/unsub?id=1" {
		t.Fatalf("URL = %q", got.URL)
	}
}

func TestParseHTTPDefersToMailto(t *testing.T) {
	got := Parse("<http://news.example.com/unsub>, <mailto:unsub@example.com>", false)
	if got.Method != MethodMailto {
		t.Fatalf("Method = %q, want mailto (http defers to mailto)", got.Method)
	}
}

func TestParseEmpty(t *testing.T) {
	got := Parse("", false)
	if got.Method != MethodNone {
		t.Fatalf("Method = %q, want none", got.Method)
	}
	got = Parse("<ftp://example.com/x>", false)
	if got.Method != MethodNone {
		t.Fatalf("Method = %q, want none for unknown scheme", got.Method)
	}
}

func TestDomainMismatch(t *testing.T) {
	cases := []struct {
		sender, host string
		want         bool
	}{
		{"news@example.com", "example.com", false},
		{"news@example.com", "news.example.com", false},             // subdomain of sender
		{"news@example.com", "unsub.example.com", false},            // subdomain of sender
		{"news@example.com", "other.com", true},                     // unrelated
		{"news@example.com", "links.sendgrid.net", false},           // known ESP
		{"news@example.com", "example.us10.list-manage.com", false}, // known ESP subdomain
		{"news@example.com", "evil-sendgrid.net.evil.com", true},    // lookalike, not ESP
		{"news@example.com", "", false},                             // nothing to compare
		{"", "other.com", false},                                    // no sender domain
		{"NEWS@EXAMPLE.COM", "EXAMPLE.com", false},                  // case-insensitive
	}
	for _, c := range cases {
		if got := DomainMismatch(c.sender, c.host); got != c.want {
			t.Errorf("DomainMismatch(%q, %q) = %v, want %v", c.sender, c.host, got, c.want)
		}
	}
}

func TestResolveFlagsMismatch(t *testing.T) {
	got := Resolve("<https://tracker.other.com/unsub>", false, "news@example.com")
	if got.Method != MethodBrowser || !got.DomainMismatch {
		t.Fatalf("got %+v, want browser + mismatch", got)
	}
}

func TestPostOneClick(t *testing.T) {
	var gotMethod, gotBody, gotContentType string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := PostOneClick(context.Background(), srv.URL+"/unsub?id=1", srv.Client()); err != nil {
		t.Fatalf("PostOneClick() error = %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody != "List-Unsubscribe=One-Click" {
		t.Errorf("body = %q, want List-Unsubscribe=One-Click", gotBody)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotContentType)
	}
}

func TestPostOneClickRejectsHTTP(t *testing.T) {
	if err := PostOneClick(context.Background(), "http://example.com/unsub", nil); err == nil {
		t.Fatal("PostOneClick(http) succeeded, want refusal")
	}
}

func TestPostOneClickNon2xx(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	if err := PostOneClick(context.Background(), srv.URL, srv.Client()); err == nil {
		t.Fatal("PostOneClick(410) succeeded, want error")
	}
}

func TestBuildMailtoMessage(t *testing.T) {
	raw, msgID, err := BuildMailtoMessage("Me", "me@example.com", "unsub@sender.example?subject=x", "unsub-123")
	if err != nil {
		t.Fatalf("BuildMailtoMessage() error = %v", err)
	}
	// The To header must carry the bare address, not the query string.
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	if got := msg.Header.Get("To"); got != "unsub@sender.example" {
		t.Errorf("To = %q, want unsub@sender.example", got)
	}
	if got := msg.Header.Get("Subject"); got != "unsub-123" {
		t.Errorf("Subject = %q, want unsub-123", got)
	}
	if got := msg.Header.Get("From"); got != "Me <me@example.com>" {
		t.Errorf("From = %q", got)
	}
	if msgID == "" || !strings.HasPrefix(msgID, "<") || !strings.HasSuffix(msgID, ">") {
		t.Errorf("Message-ID = %q, want angle-bracketed id", msgID)
	}
	if got := msg.Header.Get("Message-ID"); got != msgID {
		t.Errorf("Message-ID header = %q, want %q", got, msgID)
	}
	body, _ := io.ReadAll(msg.Body)
	if !strings.Contains(string(body), "unsubscribe") {
		t.Errorf("body %q does not mention unsubscribe", body)
	}
}

func TestBuildMailtoMessageNoName(t *testing.T) {
	raw, _, err := BuildMailtoMessage("", "me@example.com", "unsub@sender.example", "Unsubscribe")
	if err != nil {
		t.Fatalf("BuildMailtoMessage() error = %v", err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	if got := msg.Header.Get("From"); got != "me@example.com" {
		t.Errorf("From = %q, want bare address", got)
	}
}

func TestBuildMailtoMessageBadAddress(t *testing.T) {
	if _, _, err := BuildMailtoMessage("Me", "me@example.com", "not-an-address", "x"); err == nil {
		t.Fatal("BuildMailtoMessage(bad to) succeeded, want error")
	}
	if _, _, err := BuildMailtoMessage("Me", "not-an-address", "unsub@sender.example", "x"); err == nil {
		t.Fatal("BuildMailtoMessage(bad from) succeeded, want error")
	}
}

func TestParseMailtoSubjectStripsCRLF(t *testing.T) {
	// Finding 12: a %0d%0a in the mailto subject must not survive
	// into the parsed target — it would otherwise be written raw as
	// a header line by BuildMailtoMessage.
	got := Parse("<mailto:unsub@example.com?subject=hi%0d%0aBcc%3A%20victim%40example.com>", false)
	if got.Method != MethodMailto {
		t.Fatalf("Method = %q, want mailto", got.Method)
	}
	if strings.ContainsAny(got.MailtoSubject, "\r\n") {
		t.Fatalf("MailtoSubject = %q, still contains CR/LF", got.MailtoSubject)
	}
	if got.MailtoSubject != "hiBcc: victim@example.com" {
		t.Fatalf("MailtoSubject = %q, want the injected line folded into the subject text", got.MailtoSubject)
	}
}

func TestBuildMailtoMessageStripsCRLFInjection(t *testing.T) {
	// Finding 12: a crafted subject / display name carrying CRLF
	// must not inject headers into the sent message.
	raw, _, err := BuildMailtoMessage(
		"Me\r\nBcc: injected@example.com",
		"me@example.com",
		"unsub@sender.example",
		"hi\r\nBcc: victim@example.com",
	)
	if err != nil {
		t.Fatalf("BuildMailtoMessage() error = %v", err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	if got := msg.Header.Get("Bcc"); got != "" {
		t.Errorf("Bcc = %q, want no injected Bcc header", got)
	}
	if got := msg.Header.Get("Subject"); got != "hiBcc: victim@example.com" {
		t.Errorf("Subject = %q, want the injected line folded into the subject text", got)
	}
	if got := msg.Header.Get("From"); got != `"MeBcc: injected@example.com" <me@example.com>` {
		t.Errorf("From = %q, want the injected line folded into a quoted display name", got)
	}
	// The raw bytes must contain no injected header line at all.
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("raw message contains injected header line %q", line)
		}
	}
}
