package backend

// Tests for caldavProvider's PushEvent + DeleteRemote — the new transport
// surface introduced in Phase 2 Chunk 2. Uses httptest.NewServer to record
// requests sent by the provider, then asserts on method, URL, headers, and
// status-code handling (200/201/204 success, 412 → ErrConflict, 404 idempotent
// on DELETE, other → wrapped error).
//
// SyncCalendar isn't covered here — its body was lifted verbatim from the
// original Syncer.syncCalendar, and the existing calendar backend tests +
// manual sync coverage exercise it indirectly. A dedicated httptest-based
// SyncCalendar test is tracked as a follow-up.

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
)

// fakeSecrets satisfies coreapi.Secrets with a static password. Only Get is
// exercised by caldavProvider — the rest stub safely.
type fakeSecrets struct{ password string }

func (f fakeSecrets) Set(_, _ string) error       { return nil }
func (f fakeSecrets) Get(_ string) (string, error) { return f.password, nil }
func (f fakeSecrets) Delete(_ string) error       { return nil }
func (f fakeSecrets) DeleteAll() error            { return nil }

var _ coreapi.Secrets = fakeSecrets{}

// recordedRequest captures what the provider sent so tests can assert.
type recordedRequest struct {
	method      string
	path        string
	ifMatch     string
	ifNoneMatch string
	contentType string
	body        string
}

// newFakeCalDAVServer returns an httptest.Server that records each request and
// hands the response back through the supplied handler. The handler can read
// `req` and set headers + status on `w` to simulate any CalDAV server.
func newFakeCalDAVServer(t *testing.T, handler func(req recordedRequest, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		handler(recordedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			ifMatch:     r.Header.Get("If-Match"),
			ifNoneMatch: r.Header.Get("If-None-Match"),
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		}, w)
	}))
}

// newTestProvider builds a caldavProvider wired to fakeSecrets. store is nil
// because PushEvent + DeleteRemote don't touch the store.
func newTestProvider(password string) caldavProvider {
	return caldavProvider{
		store:   nil,
		secrets: fakeSecrets{password: password},
		events:  nil,
	}
}

func TestCalDAVProvider_PushEvent_CreateSuccess(t *testing.T) {
	var got recordedRequest
	srv := newFakeCalDAVServer(t, func(req recordedRequest, w http.ResponseWriter) {
		got = req
		w.Header().Set("ETag", `"server-etag-1"`)
		w.WriteHeader(http.StatusCreated)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{UID: "abc@daak-caldav", ICSBlob: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"}

	result, err := p.PushEvent(t.Context(), src, cal, ev)
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if result.ETag != `"server-etag-1"` {
		t.Errorf("ETag = %q, want %q", result.ETag, `"server-etag-1"`)
	}
	if got.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", got.method)
	}
	if got.ifNoneMatch != "*" {
		t.Errorf("If-None-Match = %q, want *", got.ifNoneMatch)
	}
	if got.ifMatch != "" {
		t.Errorf("If-Match should be empty on create, got %q", got.ifMatch)
	}
	if !strings.HasSuffix(got.path, "/abc@daak-caldav.ics") {
		t.Errorf("path = %q, want suffix /abc@daak-caldav.ics", got.path)
	}
	if !strings.HasPrefix(got.contentType, "text/calendar") {
		t.Errorf("Content-Type = %q, want text/calendar prefix", got.contentType)
	}
	if got.body == "" {
		t.Errorf("body should not be empty")
	}
}

func TestCalDAVProvider_PushEvent_UpdateSuccess(t *testing.T) {
	var got recordedRequest
	srv := newFakeCalDAVServer(t, func(req recordedRequest, w http.ResponseWriter) {
		got = req
		w.Header().Set("ETag", `"server-etag-2"`)
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{
		UID:     "abc@daak-caldav",
		Href:    srv.URL + "/calendars/user/personal/abc.ics",
		ETag:    `"old-etag"`,
		ICSBlob: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
	}

	result, err := p.PushEvent(t.Context(), src, cal, ev)
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if result.ETag != `"server-etag-2"` {
		t.Errorf("ETag = %q, want %q", result.ETag, `"server-etag-2"`)
	}
	if got.ifMatch != `"old-etag"` {
		t.Errorf("If-Match = %q, want %q", got.ifMatch, `"old-etag"`)
	}
	if got.ifNoneMatch != "" {
		t.Errorf("If-None-Match should be empty on update, got %q", got.ifNoneMatch)
	}
	if got.path != "/calendars/user/personal/abc.ics" {
		t.Errorf("path = %q, want /calendars/user/personal/abc.ics", got.path)
	}
}

func TestCalDAVProvider_PushEvent_Conflict(t *testing.T) {
	srv := newFakeCalDAVServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{
		UID:     "abc@daak-caldav",
		Href:    srv.URL + "/calendars/user/personal/abc.ics",
		ETag:    `"stale-etag"`,
		ICSBlob: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
	}

	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict", err)
	}
}

func TestCalDAVProvider_PushEvent_ServerError(t *testing.T) {
	srv := newFakeCalDAVServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{UID: "abc@daak-caldav", ICSBlob: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"}

	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if err == nil {
		t.Fatal("PushEvent expected error on 500, got nil")
	}
	if errors.Is(err, ErrConflict) {
		t.Errorf("PushEvent returned ErrConflict on 500, want generic error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500, got: %v", err)
	}
}

func TestCalDAVProvider_DeleteRemote_Success(t *testing.T) {
	var got recordedRequest
	srv := newFakeCalDAVServer(t, func(req recordedRequest, w http.ResponseWriter) {
		got = req
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{
		UID:  "abc@daak-caldav",
		Href: srv.URL + "/calendars/user/personal/abc.ics",
		ETag: `"current-etag"`,
	}

	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if got.ifMatch != `"current-etag"` {
		t.Errorf("If-Match = %q, want %q", got.ifMatch, `"current-etag"`)
	}
	if got.path != "/calendars/user/personal/abc.ics" {
		t.Errorf("path = %q, want /calendars/user/personal/abc.ics", got.path)
	}
}

func TestCalDAVProvider_DeleteRemote_NotFoundIdempotent(t *testing.T) {
	srv := newFakeCalDAVServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{Href: srv.URL + "/calendars/user/personal/gone.ics", ETag: `"x"`}

	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Errorf("DeleteRemote on 404 should be idempotent, got: %v", err)
	}
}

func TestCalDAVProvider_DeleteRemote_Conflict(t *testing.T) {
	srv := newFakeCalDAVServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{Href: srv.URL + "/calendars/user/personal/abc.ics", ETag: `"stale"`}

	err := p.DeleteRemote(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Errorf("DeleteRemote err = %v, want ErrConflict", err)
	}
}

func TestCalDAVProvider_DeleteRemote_NoHrefSkips(t *testing.T) {
	// If ev.Href is empty (event was never on server, sync hadn't run),
	// DeleteRemote should be a no-op so the local delete still proceeds.
	called := false
	srv := newFakeCalDAVServer(t, func(_ recordedRequest, _ http.ResponseWriter) {
		called = true
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	ev := Event{} // empty Href

	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Errorf("DeleteRemote with empty Href should be nil, got: %v", err)
	}
	if called {
		t.Errorf("DeleteRemote with empty Href should not call the server")
	}
}

func TestCalDAVProvider_PushEvent_RelativeCalURLNextcloud(t *testing.T) {
	// Nextcloud (and other CalDAV servers) return calendar paths as
	// server-relative (no scheme/host). Regression test: PushEvent must
	// resolve the relative path against src.URL before issuing PUT, or
	// http.Request errors with "unsupported protocol scheme".
	var got recordedRequest
	srv := newFakeCalDAVServer(t, func(req recordedRequest, w http.ResponseWriter) {
		got = req
		w.Header().Set("ETag", `"nc-etag"`)
		w.WriteHeader(http.StatusCreated)
	})
	defer srv.Close()

	p := newTestProvider("secret")
	src := Source{
		ID:       "src-nc",
		Type:     SourceTypeCalDAV,
		Username: "user",
		// src.URL is absolute (server scheme+host) — typical of homePath
		// returned by Nextcloud's CalDAV discovery.
		URL: srv.URL + "/remote.php/dav/calendars/user/",
	}
	cal := Calendar{
		ID: "cal-nc",
		// cal.URL is server-relative — typical of Nextcloud's PROPFIND
		// multistatus responses.
		URL: "/remote.php/dav/calendars/user/personal/",
	}
	ev := Event{UID: "evt@daak-caldav", ICSBlob: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"}

	if _, err := p.PushEvent(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if got.path != "/remote.php/dav/calendars/user/personal/evt@daak-caldav.ics" {
		t.Errorf("path = %q, want /remote.php/dav/calendars/user/personal/evt@daak-caldav.ics", got.path)
	}
}

func TestAbsoluteHref(t *testing.T) {
	tests := []struct {
		name    string
		srcURL  string
		href    string
		want    string
		wantErr bool
	}{
		{
			name:   "href already absolute",
			srcURL: "https://cloud.example.com/remote.php/dav/calendars/user/",
			href:   "https://cloud.example.com/remote.php/dav/calendars/user/personal/abc.ics",
			want:   "https://cloud.example.com/remote.php/dav/calendars/user/personal/abc.ics",
		},
		{
			name:   "relative href + absolute srcURL (Nextcloud)",
			srcURL: "https://cloud.example.com/remote.php/dav/calendars/user/",
			href:   "/remote.php/dav/calendars/user/personal/abc.ics",
			want:   "https://cloud.example.com/remote.php/dav/calendars/user/personal/abc.ics",
		},
		{
			name:    "relative href + relative srcURL (broken — should error)",
			srcURL:  "/calendars/user/",
			href:    "/calendars/user/personal/abc.ics",
			wantErr: true,
		},
		{
			name:   "srcURL with port",
			srcURL: "https://cloud.example.com:8443/remote.php/dav/",
			href:   "/remote.php/dav/calendars/user/personal/abc.ics",
			want:   "https://cloud.example.com:8443/remote.php/dav/calendars/user/personal/abc.ics",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := absoluteHref(tt.srcURL, tt.href)
			if tt.wantErr {
				if err == nil {
					t.Errorf("absoluteHref(%q, %q) = %q, want error", tt.srcURL, tt.href, got)
				}
				return
			}
			if err != nil {
				t.Errorf("absoluteHref(%q, %q) error = %v", tt.srcURL, tt.href, err)
				return
			}
			if got != tt.want {
				t.Errorf("absoluteHref(%q, %q) = %q, want %q", tt.srcURL, tt.href, got, tt.want)
			}
		})
	}
}

func TestJoinHref(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		suffix string
		want   string
	}{
		{
			name:   "with scheme + host + trailing slash",
			base:   "https://caldav.example.com/calendars/user/personal/",
			suffix: "event-1.ics",
			want:   "https://caldav.example.com/calendars/user/personal/event-1.ics",
		},
		{
			name:   "with scheme + host + no trailing slash",
			base:   "https://caldav.example.com/calendars/user/personal",
			suffix: "event-1.ics",
			want:   "https://caldav.example.com/calendars/user/personal/event-1.ics",
		},
		{
			name:   "host only",
			base:   "https://caldav.example.com",
			suffix: "event-1.ics",
			want:   "https://caldav.example.com/event-1.ics",
		},
		{
			name:   "no scheme",
			base:   "/calendars/user/personal/",
			suffix: "event-1.ics",
			want:   "/calendars/user/personal/event-1.ics",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinHref(tt.base, tt.suffix)
			if got != tt.want {
				t.Errorf("joinHref(%q, %q) = %q, want %q", tt.base, tt.suffix, got, tt.want)
			}
		})
	}
}

// --- Finding 5: 412 handling ----------------------------------------------
//
// A 412 used to trigger an unconditional retry of the PUT/DELETE, silently
// overwriting (or deleting) another person's edit to a shared calendar.
// Now the provider re-fetches the server copy first and retries
// unconditionally ONLY if it is substantively identical to the locally
// stored version — i.e. the 412 came from our own stale ETag. Otherwise it
// returns ErrConflict and the server sees no second write at all.

const conflictTestICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Daak//Test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:evt-1@daak-caldav\r\nDTSTAMP:20261124T000000Z\r\n" +
	"DTSTART:20261124T100000Z\r\nDTEND:20261124T110000Z\r\n" +
	"SUMMARY:Team sync\r\nDESCRIPTION:Weekly sync\r\nLOCATION:Room 1\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// Same event, edited by someone else on the server: different summary and
// a moved start time.
const conflictTestICSChanged = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Daak//Test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:evt-1@daak-caldav\r\nDTSTAMP:20261124T000000Z\r\n" +
	"DTSTART:20261124T120000Z\r\nDTEND:20261124T130000Z\r\n" +
	"SUMMARY:Team sync (moved by someone else)\r\nDESCRIPTION:Weekly sync\r\nLOCATION:Room 1\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// conflictFakeServer serves the 412 dance: a conditional write (If-Match
// set) gets 412, a GET returns the configured server copy, and an
// unconditional write succeeds. Every request is recorded so tests can
// prove whether an unconditional retry happened.
type conflictFakeServer struct {
	srv       *httptest.Server
	requests  []recordedRequest
	serverICS string
}

func newConflictFakeServer(t *testing.T, serverICS string) *conflictFakeServer {
	t.Helper()
	fs := &conflictFakeServer{serverICS: serverICS}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		fs.requests = append(fs.requests, recordedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			ifMatch:     r.Header.Get("If-Match"),
			ifNoneMatch: r.Header.Get("If-None-Match"),
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		})
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-Match") != "" {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			w.Header().Set("ETag", `"new-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if r.Header.Get("If-Match") != "" {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, fs.serverICS)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return fs
}

func (fs *conflictFakeServer) countMethod(method string) int {
	n := 0
	for _, req := range fs.requests {
		if req.method == method {
			n++
		}
	}
	return n
}

// seedCalDAVStoredEvent stores the event as of the last sync — the local row
// the provider compares the re-fetched server copy against — and returns the
// source + calendar to call the provider with.
func seedCalDAVStoredEvent(t *testing.T, store *Store, srvURL string) (Source, Calendar) {
	t.Helper()
	srcID, calID := "src-conflict", "cal-conflict"
	now := time.Now().Unix()
	if err := store.WithTx(func(tx *sql.Tx) error {
		if err := store.CreateSourceTx(tx, Source{
			ID: srcID, Type: SourceTypeCalDAV, Name: "Test",
			Enabled: true, Writable: true, CreatedAt: now,
		}); err != nil {
			return err
		}
		return store.CreateCalendarTx(tx, Calendar{
			ID: calID, SourceID: srcID, URL: srvURL + "/calendars/user/personal/",
			DisplayName: "Personal", Visible: true, CreatedAt: now,
		})
	}); err != nil {
		t.Fatalf("seed source/calendar: %v", err)
	}
	parsed, err := ParseCalendarObject(conflictTestICS)
	if err != nil {
		t.Fatalf("parse seed ICS: %v", err)
	}
	stored := parsed.Master
	stored.ID = "ev-1"
	stored.CalendarID = calID
	stored.ETag = `"stale-etag"`
	stored.Href = "/calendars/user/personal/evt-1@daak-caldav.ics"
	stored.ICSBlob = conflictTestICS
	if err := store.WithTx(func(tx *sql.Tx) error {
		return store.UpsertEventTx(tx, stored)
	}); err != nil {
		t.Fatalf("seed stored event: %v", err)
	}
	src := Source{ID: srcID, Type: SourceTypeCalDAV, Username: "user", URL: srvURL}
	cal := Calendar{ID: calID, URL: srvURL + "/calendars/user/personal/"}
	return src, cal
}

func TestCalDAVProvider_PushEvent_Update412_ChangedServerCopyIsConflict(t *testing.T) {
	store := newTestStore(t)
	fs := newConflictFakeServer(t, conflictTestICSChanged)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	// The user's edit: a new summary, pushed with the stale local ETag.
	ev := Event{
		UID:     "evt-1@daak-caldav",
		Href:    "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag:    `"stale-etag"`,
		ICSBlob: strings.Replace(conflictTestICS, "SUMMARY:Team sync", "SUMMARY:Team sync (my edit)", 1),
	}
	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict", err)
	}
	// The server must have seen exactly one PUT (the conditional one) and
	// the re-fetch GET — no unconditional retry overwrote the other edit.
	if got := fs.countMethod(http.MethodPut); got != 1 {
		t.Fatalf("server saw %d PUTs, want 1 (no unconditional retry); requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1 (re-fetch to verify); requests=%+v", got, fs.requests)
	}
}

func TestCalDAVProvider_PushEvent_Update412_UnchangedServerCopyRetriesUnconditional(t *testing.T) {
	store := newTestStore(t)
	fs := newConflictFakeServer(t, conflictTestICS)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:     "evt-1@daak-caldav",
		Href:    "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag:    `"stale-etag"`,
		ICSBlob: strings.Replace(conflictTestICS, "SUMMARY:Team sync", "SUMMARY:Team sync (my edit)", 1),
	}
	result, err := p.PushEvent(t.Context(), src, cal, ev)
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if result.ETag != `"new-etag"` {
		t.Errorf("ETag = %q, want %q", result.ETag, `"new-etag"`)
	}
	// Conditional PUT 412s, GET proves the server copy is our own stale
	// one, unconditional PUT lands the edit.
	if got := fs.countMethod(http.MethodPut); got != 2 {
		t.Fatalf("server saw %d PUTs, want 2 (conditional + verified retry); requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1; requests=%+v", got, fs.requests)
	}
	if fs.requests[1].ifMatch != "" {
		t.Errorf("retry PUT carried If-Match %q, want none", fs.requests[1].ifMatch)
	}
}

func TestCalDAVProvider_DeleteRemote_412_ChangedServerCopyIsConflict(t *testing.T) {
	store := newTestStore(t)
	fs := newConflictFakeServer(t, conflictTestICSChanged)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:  "evt-1@daak-caldav",
		Href: "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag: `"stale-etag"`,
	}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteRemote err = %v, want ErrConflict", err)
	}
	// Exactly one DELETE (the conditional one) + the re-fetch GET: the
	// other person's version was not deleted.
	if got := fs.countMethod(http.MethodDelete); got != 1 {
		t.Fatalf("server saw %d DELETEs, want 1 (no unconditional retry); requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1; requests=%+v", got, fs.requests)
	}
}

func TestCalDAVProvider_DeleteRemote_412_UnchangedServerCopyRetriesUnconditional(t *testing.T) {
	store := newTestStore(t)
	fs := newConflictFakeServer(t, conflictTestICS)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:  "evt-1@daak-caldav",
		Href: "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag: `"stale-etag"`,
	}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if got := fs.countMethod(http.MethodDelete); got != 2 {
		t.Fatalf("server saw %d DELETEs, want 2 (conditional + verified retry); requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1; requests=%+v", got, fs.requests)
	}
}
