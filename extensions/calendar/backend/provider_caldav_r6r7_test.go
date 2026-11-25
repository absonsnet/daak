package backend

// Tests for re-audit findings R6 + R7 (Nov 24 re-audit of v0.7.38).
//
// R6: sameCalendarSubstance must detect concurrent edits that only touch
// STATUS, the organiser, SEQUENCE, CATEGORIES, EXDATE, or VALARMs — and
// the canonical-fingerprint backstop must catch changes to properties the
// parser does not model at all, without tripping on formatting-only
// differences (property order, DTSTAMP rewrites, line folding).
//
// R7: a CalDAV update/delete with no known local ETag must re-fetch and
// compare the server copy before writing unconditionally, and refuse with
// ErrConflict when the server copy moved (or cannot be verified).

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- R6: sameCalendarSubstance ----------------------------------------------

const r6BaseICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Daak//Test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:evt-r6@daak-caldav\r\nDTSTAMP:20261124T000000Z\r\n" +
	"DTSTART:20261124T100000Z\r\nDTEND:20261124T110000Z\r\n" +
	"SUMMARY:Team sync\r\nDESCRIPTION:Weekly sync\r\nLOCATION:Room 1\r\n" +
	"STATUS:CONFIRMED\r\nSEQUENCE:2\r\nCATEGORIES:MEETING,TEAM\r\n" +
	"ORGANIZER;CN=Boss:mailto:boss@example.com\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=MO\r\nEXDATE:20261207T100000Z\r\n" +
	"BEGIN:VALARM\r\nTRIGGER:-PT15M\r\nACTION:DISPLAY\r\nDESCRIPTION:Reminder\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestSameCalendarSubstance_R6ChangedFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name:   "server-side cancellation",
			mutate: func(s string) string { return strings.Replace(s, "STATUS:CONFIRMED", "STATUS:CANCELLED", 1) },
		},
		{
			name: "organiser changed",
			mutate: func(s string) string {
				return strings.Replace(s, "mailto:boss@example.com", "mailto:other@example.com", 1)
			},
		},
		{
			name: "organiser removed",
			mutate: func(s string) string {
				return strings.Replace(s, "ORGANIZER;CN=Boss:mailto:boss@example.com\r\n", "", 1)
			},
		},
		{
			name:   "sequence bumped",
			mutate: func(s string) string { return strings.Replace(s, "SEQUENCE:2", "SEQUENCE:3", 1) },
		},
		{
			name:   "category dropped",
			mutate: func(s string) string { return strings.Replace(s, "CATEGORIES:MEETING,TEAM", "CATEGORIES:MEETING", 1) },
		},
		{
			name:   "exception date removed (deleted occurrence restored)",
			mutate: func(s string) string { return strings.Replace(s, "EXDATE:20261207T100000Z\r\n", "", 1) },
		},
		{
			name: "exception date moved",
			mutate: func(s string) string {
				return strings.Replace(s, "EXDATE:20261207T100000Z", "EXDATE:20261214T100000Z", 1)
			},
		},
		{
			name:   "alarm trigger changed",
			mutate: func(s string) string { return strings.Replace(s, "TRIGGER:-PT15M", "TRIGGER:-PT30M", 1) },
		},
		{
			name: "alarm removed",
			mutate: func(s string) string {
				return strings.Replace(s, "BEGIN:VALARM\r\nTRIGGER:-PT15M\r\nACTION:DISPLAY\r\nDESCRIPTION:Reminder\r\nEND:VALARM\r\n", "", 1)
			},
		},
		{
			name: "unmodelled RDATE added (backstop)",
			mutate: func(s string) string {
				return strings.Replace(s, "EXDATE:20261207T100000Z", "EXDATE:20261207T100000Z\r\nRDATE:20261208T100000Z", 1)
			},
		},
		{
			name: "unmodelled vendor prop added (backstop)",
			mutate: func(s string) string {
				return strings.Replace(s, "LOCATION:Room 1", "LOCATION:Room 1\r\nX-CUSTOM:added-by-server", 1)
			},
		},
		{
			name: "unmodelled HTML body changed (backstop)",
			mutate: func(s string) string {
				return strings.Replace(s, "DESCRIPTION:Weekly sync", "DESCRIPTION:Weekly sync\r\nX-ALT-DESC;FMTTYPE=text/html:<p>Weekly sync</p>", 1)
			},
		},
	}
	base, err := ParseCalendarObject(r6BaseICS)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other, err := ParseCalendarObject(tc.mutate(r6BaseICS))
			if err != nil {
				t.Fatalf("parse mutated: %v", err)
			}
			if sameCalendarSubstance(base, other) {
				t.Errorf("sameCalendarSubstance = true, want false for %s", tc.name)
			}
			if sameCalendarSubstance(other, base) {
				t.Errorf("sameCalendarSubstance (reversed) = true, want false for %s", tc.name)
			}
		})
	}
}

func TestSameCalendarSubstance_R6FormattingOnlyIsUnchanged(t *testing.T) {
	// Same substance, different bytes: DTSTAMP rewritten, properties
	// reordered, CATEGORIES + EXDATE lists reordered, DESCRIPTION folded.
	reformatted := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Other//Server//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:evt-r6@daak-caldav\r\nDTSTAMP:20261125T120000Z\r\n" +
		"LOCATION:Room 1\r\nSUMMARY:Team sync\r\nDESCRIPTION:Weekly \r\n sync\r\n" +
		"STATUS:CONFIRMED\r\nSEQUENCE:2\r\nCATEGORIES:TEAM,MEETING\r\n" +
		"ORGANIZER;CN=Boss:mailto:boss@example.com\r\n" +
		"DTSTART:20261124T100000Z\r\nDTEND:20261124T110000Z\r\n" +
		"EXDATE:20261207T100000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Reminder\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	base, err := ParseCalendarObject(r6BaseICS)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	other, err := ParseCalendarObject(reformatted)
	if err != nil {
		t.Fatalf("parse reformatted: %v", err)
	}
	if !sameCalendarSubstance(base, other) {
		t.Errorf("sameCalendarSubstance = false for a formatting-only difference, want true")
	}
}

// --- R6 end-to-end: 412 with a cancelled server copy ------------------------

// TestCalDAVProvider_PushEvent_Update412_CancelledServerCopyIsConflict proves
// the R6 headline case through the real write path: the server copy was
// cancelled by someone else, the stale-ETag PUT 412s, and the provider must
// surface ErrConflict without an unconditional retry overwriting the
// cancellation.
func TestCalDAVProvider_PushEvent_Update412_CancelledServerCopyIsConflict(t *testing.T) {
	store := newTestStore(t)
	cancelled := strings.Replace(conflictTestICS, "SUMMARY:Team sync",
		"SUMMARY:Team sync\r\nSTATUS:CANCELLED", 1)
	fs := newConflictFakeServer(t, cancelled)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

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
	if got := fs.countMethod(http.MethodPut); got != 1 {
		t.Fatalf("server saw %d PUTs, want 1 (no unconditional retry over a cancellation); requests=%+v", got, fs.requests)
	}
}

// TestCalDAVProvider_PushEvent_Update412_ReformattedServerCopyRetries proves
// the R6 backstop does not false-positive: a server copy that differs only
// in formatting (DTSTAMP, property order) still retries unconditionally.
func TestCalDAVProvider_PushEvent_Update412_ReformattedServerCopyRetries(t *testing.T) {
	store := newTestStore(t)
	reformatted := strings.Replace(conflictTestICS, "DTSTAMP:20261124T000000Z", "DTSTAMP:20261125T120000Z", 1)
	reformatted = strings.Replace(reformatted, "SUMMARY:Team sync\r\nDESCRIPTION:Weekly sync\r\nLOCATION:Room 1\r\n",
		"LOCATION:Room 1\r\nSUMMARY:Team sync\r\nDESCRIPTION:Weekly sync\r\n", 1)
	fs := newConflictFakeServer(t, reformatted)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:     "evt-1@daak-caldav",
		Href:    "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag:    `"stale-etag"`,
		ICSBlob: strings.Replace(conflictTestICS, "SUMMARY:Team sync", "SUMMARY:Team sync (my edit)", 1),
	}
	if _, err := p.PushEvent(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if got := fs.countMethod(http.MethodPut); got != 2 {
		t.Fatalf("server saw %d PUTs, want 2 (conditional + verified retry); requests=%+v", got, fs.requests)
	}
}

// --- R7: empty local ETag ---------------------------------------------------

// emptyETagFakeServer records every request and serves a configurable
// server copy on GET. PUT/DELETE without an ETag in the response model the
// server that omits ETags (PUT 200 with no ETag header, HEAD 404).
type emptyETagFakeServer struct {
	srv       *httptest.Server
	requests  []recordedRequest
	serverICS string
	getStatus int // 0 = 200
}

func newEmptyETagFakeServer(t *testing.T, serverICS string, getStatus int) *emptyETagFakeServer {
	t.Helper()
	fs := &emptyETagFakeServer{serverICS: serverICS, getStatus: getStatus}
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
		case http.MethodGet:
			if fs.getStatus != 0 {
				w.WriteHeader(fs.getStatus)
				return
			}
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, fs.serverICS)
		case http.MethodPut:
			// No ETag header: the server that omits it on PUT responses.
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return fs
}

func (fs *emptyETagFakeServer) countMethod(method string) int {
	n := 0
	for _, req := range fs.requests {
		if req.method == method {
			n++
		}
	}
	return n
}

// emptyETagEvent builds the caller's edit: no local ETag, Href set (update).
func emptyETagEvent() Event {
	return Event{
		UID:     "evt-1@daak-caldav",
		Href:    "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag:    "",
		ICSBlob: strings.Replace(conflictTestICS, "SUMMARY:Team sync", "SUMMARY:Team sync (my edit)", 1),
	}
}

func TestCalDAVProvider_PushEvent_EmptyETag_UnchangedServerCopyProceeds(t *testing.T) {
	store := newTestStore(t)
	fs := newEmptyETagFakeServer(t, conflictTestICS, 0)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	result, err := p.PushEvent(t.Context(), src, cal, emptyETagEvent())
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if result.ETag != "" {
		t.Errorf("ETag = %q, want empty (server omits it, HEAD fails)", result.ETag)
	}
	// One re-fetch GET proving the server copy unchanged, then exactly one
	// unconditional PUT — and that PUT must carry no If-Match.
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1 (re-fetch before unconditional PUT); requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodPut); got != 1 {
		t.Fatalf("server saw %d PUTs, want 1; requests=%+v", got, fs.requests)
	}
	if fs.requests[1].ifMatch != "" {
		t.Errorf("PUT carried If-Match %q, want none (no local ETag)", fs.requests[1].ifMatch)
	}
}

func TestCalDAVProvider_PushEvent_EmptyETag_ChangedServerCopyIsConflict(t *testing.T) {
	store := newTestStore(t)
	fs := newEmptyETagFakeServer(t, conflictTestICSChanged, 0)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	_, err := p.PushEvent(t.Context(), src, cal, emptyETagEvent())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict", err)
	}
	if got := fs.countMethod(http.MethodPut); got != 0 {
		t.Fatalf("server saw %d PUTs, want 0 (changed copy must not be overwritten blindly); requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1; requests=%+v", got, fs.requests)
	}
}

func TestCalDAVProvider_PushEvent_EmptyETag_GetFailsIsConflict(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := newTestStore(t)
			fs := newEmptyETagFakeServer(t, conflictTestICS, status)
			defer fs.srv.Close()
			src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
			p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

			_, err := p.PushEvent(t.Context(), src, cal, emptyETagEvent())
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("PushEvent err = %v, want ErrConflict", err)
			}
			if got := fs.countMethod(http.MethodPut); got != 0 {
				t.Fatalf("server saw %d PUTs, want 0 (unverifiable copy must not be overwritten); requests=%+v", got, fs.requests)
			}
		})
	}
}

func TestCalDAVProvider_PushEvent_EmptyETag_NoStoreIsConflict(t *testing.T) {
	// No store wired: the comparison cannot be made at all, so the write
	// must be refused rather than sent bare.
	fs := newEmptyETagFakeServer(t, conflictTestICS, 0)
	defer fs.srv.Close()
	p := newTestProvider("secret")
	src := Source{ID: "src-1", Type: SourceTypeCalDAV, Username: "user", URL: fs.srv.URL}
	cal := Calendar{ID: "cal-1", URL: fs.srv.URL + "/calendars/user/personal/"}

	_, err := p.PushEvent(t.Context(), src, cal, emptyETagEvent())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict", err)
	}
	if got := fs.countMethod(http.MethodPut); got != 0 {
		t.Fatalf("server saw %d PUTs, want 0; requests=%+v", got, fs.requests)
	}
}

func TestCalDAVProvider_DeleteRemote_EmptyETag_UnchangedProceeds(t *testing.T) {
	store := newTestStore(t)
	fs := newEmptyETagFakeServer(t, conflictTestICS, 0)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:  "evt-1@daak-caldav",
		Href: "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag: "",
	}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if got := fs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1; requests=%+v", got, fs.requests)
	}
	if got := fs.countMethod(http.MethodDelete); got != 1 {
		t.Fatalf("server saw %d DELETEs, want 1; requests=%+v", got, fs.requests)
	}
}

func TestCalDAVProvider_DeleteRemote_EmptyETag_ChangedIsConflict(t *testing.T) {
	store := newTestStore(t)
	fs := newEmptyETagFakeServer(t, conflictTestICSChanged, 0)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:  "evt-1@daak-caldav",
		Href: "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag: "",
	}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteRemote err = %v, want ErrConflict", err)
	}
	if got := fs.countMethod(http.MethodDelete); got != 0 {
		t.Fatalf("server saw %d DELETEs, want 0; requests=%+v", got, fs.requests)
	}
}

func TestCalDAVProvider_DeleteRemote_EmptyETag_GoneServerCopyProceeds(t *testing.T) {
	// The server copy is already gone: the delete is a no-op success.
	store := newTestStore(t)
	fs := newEmptyETagFakeServer(t, conflictTestICS, http.StatusNotFound)
	defer fs.srv.Close()
	src, cal := seedCalDAVStoredEvent(t, store, fs.srv.URL)
	p := caldavProvider{store: store, secrets: fakeSecrets{password: "secret"}}

	ev := Event{
		UID:  "evt-1@daak-caldav",
		Href: "/calendars/user/personal/evt-1@daak-caldav.ics",
		ETag: "",
	}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if got := fs.countMethod(http.MethodDelete); got != 1 {
		t.Fatalf("server saw %d DELETEs, want 1; requests=%+v", got, fs.requests)
	}
}
