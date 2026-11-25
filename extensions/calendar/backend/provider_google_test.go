package backend

// Tests for googleProvider: translation round-trip + PushEvent/DeleteRemote
// HTTP-level behavior. The translation tests verify ICS ↔ Google JSON
// preserves the load-bearing fields (UID, start/end, RRULE, VALARM); the
// HTTP tests use httptest.Server with a URL-rewriting transport so the
// production code's googleAPIBase const stays untouched.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
)

// --- Fake Auth ---------------------------------------------------------------

// fakeAuth implements coreapi.Auth by returning an http.Client whose
// transport rewrites googleAPIBase URLs to the given httptest.Server URL.
// All other methods are unused stubs.
type fakeAuth struct{ target string }

func (f fakeAuth) HTTPClient(_ string, _ []coreapi.AuthScope) (*http.Client, error) {
	return &http.Client{Transport: rewriteTransport(f)}, nil
}

func (fakeAuth) IMAPClient(_ string, _ []string) (coreapi.IMAPClient, error) {
	return nil, errors.New("fakeAuth: IMAPClient not implemented")
}

func (fakeAuth) SMTPClient(_ string) (coreapi.SMTPClient, error) {
	return nil, errors.New("fakeAuth: SMTPClient not implemented")
}

func (fakeAuth) StartIncrementalConsent(_ coreapi.StartIncrementalConsentRequest) error {
	return errors.New("fakeAuth: StartIncrementalConsent not implemented")
}

var _ coreapi.Auth = fakeAuth{}

// rewriteTransport rewrites googleAPIBase to a test target.
type rewriteTransport struct{ target string }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.String(), googleAPIBase) {
		newURL := r.target + strings.TrimPrefix(req.URL.String(), googleAPIBase)
		u, err := url.Parse(newURL)
		if err != nil {
			return nil, err
		}
		req.URL = u
		req.Host = u.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}

func newTestGoogleProvider(serverURL string) googleProvider {
	return googleProvider{
		store: nil, // PushEvent + DeleteRemote don't touch store
		auth:  fakeAuth{target: serverURL},
	}
}

// --- Translation: round-trip ------------------------------------------------

func TestGoogleTranslate_NonRecurringTimedRoundTrip(t *testing.T) {
	src := googleEvent{
		ICalUID:     "evt-uid-1@daak-google",
		Status:      "confirmed",
		Summary:     "Project sync",
		Description: "Weekly project status",
		Location:    "Room 4B",
		Start: &googleTimePoint{
			DateTime: "2026-06-10T14:00:00-07:00",
			TimeZone: "America/Los_Angeles",
		},
		End: &googleTimePoint{
			DateTime: "2026-06-10T15:00:00-07:00",
			TimeZone: "America/Los_Angeles",
		},
	}

	blob, err := translateGoogleEventToICS(src)
	if err != nil {
		t.Fatalf("translateGoogleEventToICS: %v", err)
	}
	if !strings.Contains(blob, "UID:evt-uid-1@daak-google") {
		t.Errorf("blob missing UID:\n%s", blob)
	}
	if !strings.Contains(blob, "SUMMARY:Project sync") {
		t.Errorf("blob missing SUMMARY")
	}
	if !strings.Contains(blob, "TZID=America/Los_Angeles") {
		t.Errorf("blob missing TZID:\n%s", blob)
	}

	back, err := translateICSToGoogleJSON(blob)
	if err != nil {
		t.Fatalf("translateICSToGoogleJSON: %v", err)
	}
	if back.ICalUID != src.ICalUID {
		t.Errorf("UID round-trip: got %q, want %q", back.ICalUID, src.ICalUID)
	}
	if back.Summary != src.Summary {
		t.Errorf("Summary round-trip: got %q, want %q", back.Summary, src.Summary)
	}
	if back.Description != src.Description {
		t.Errorf("Description round-trip: got %q, want %q", back.Description, src.Description)
	}
	if back.Location != src.Location {
		t.Errorf("Location round-trip: got %q, want %q", back.Location, src.Location)
	}
	if back.Start == nil || back.End == nil {
		t.Fatalf("Start/End missing on round-trip")
	}
	if back.Start.TimeZone != src.Start.TimeZone {
		t.Errorf("Start TZ round-trip: got %q, want %q", back.Start.TimeZone, src.Start.TimeZone)
	}
	// Parse both DateTimes to compare instants (formats may differ slightly).
	srcStart, _ := time.Parse(time.RFC3339, src.Start.DateTime)
	backStart, _ := time.Parse(time.RFC3339, back.Start.DateTime)
	if !srcStart.Equal(backStart) {
		t.Errorf("Start instant: got %v, want %v", backStart, srcStart)
	}
}

func TestGoogleTranslate_AllDay(t *testing.T) {
	src := googleEvent{
		ICalUID: "alld@daak-google",
		Status:  "confirmed",
		Summary: "Holiday",
		Start:   &googleTimePoint{Date: "2026-07-04"},
		End:     &googleTimePoint{Date: "2026-07-05"},
	}
	blob, err := translateGoogleEventToICS(src)
	if err != nil {
		t.Fatalf("translateGoogleEventToICS: %v", err)
	}
	if !strings.Contains(blob, "DTSTART;VALUE=DATE:20260704") {
		t.Errorf("blob missing all-day DTSTART:\n%s", blob)
	}

	back, err := translateICSToGoogleJSON(blob)
	if err != nil {
		t.Fatalf("translateICSToGoogleJSON: %v", err)
	}
	if back.Start.Date != "2026-07-04" {
		t.Errorf("Start.Date: got %q, want 2026-07-04", back.Start.Date)
	}
	if back.End.Date != "2026-07-05" {
		t.Errorf("End.Date: got %q, want 2026-07-05", back.End.Date)
	}
	if back.Start.DateTime != "" {
		t.Errorf("Start.DateTime should be empty for all-day, got %q", back.Start.DateTime)
	}
}

func TestGoogleTranslate_RecurringWithReminder(t *testing.T) {
	src := googleEvent{
		ICalUID: "rec@daak-google",
		Status:  "confirmed",
		Summary: "Standup",
		Start: &googleTimePoint{
			DateTime: "2026-06-08T09:00:00Z",
			TimeZone: "UTC",
		},
		End: &googleTimePoint{
			DateTime: "2026-06-08T09:30:00Z",
			TimeZone: "UTC",
		},
		Recurrence: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO"},
		Reminders: &googleReminders{
			UseDefault: false,
			Overrides:  []googleReminderOverride{{Method: "popup", Minutes: 15}},
		},
	}
	blob, err := translateGoogleEventToICS(src)
	if err != nil {
		t.Fatalf("translateGoogleEventToICS: %v", err)
	}
	if !strings.Contains(blob, "RRULE:FREQ=WEEKLY;BYDAY=MO") {
		t.Errorf("blob missing RRULE:\n%s", blob)
	}
	if !strings.Contains(blob, "BEGIN:VALARM") {
		t.Errorf("blob missing VALARM:\n%s", blob)
	}
	if !strings.Contains(blob, "TRIGGER:-PT15M") {
		t.Errorf("blob missing TRIGGER -PT15M:\n%s", blob)
	}

	back, err := translateICSToGoogleJSON(blob)
	if err != nil {
		t.Fatalf("translateICSToGoogleJSON: %v", err)
	}
	if len(back.Recurrence) != 1 || back.Recurrence[0] != "RRULE:FREQ=WEEKLY;BYDAY=MO" {
		t.Errorf("Recurrence round-trip: got %v", back.Recurrence)
	}
	if back.Reminders == nil || len(back.Reminders.Overrides) != 1 {
		t.Fatalf("Reminders not round-tripped: %+v", back.Reminders)
	}
	if back.Reminders.Overrides[0].Minutes != 15 {
		t.Errorf("Reminder minutes: got %d, want 15", back.Reminders.Overrides[0].Minutes)
	}
}

func TestGoogleTranslate_CancelledReturnsSentinel(t *testing.T) {
	_, err := translateGoogleEventToICS(googleEvent{
		ICalUID: "x@daak-google",
		Status:  "cancelled",
	})
	if !errors.Is(err, errGoogleEventCancelled) {
		t.Errorf("translateGoogleEventToICS on cancelled = %v, want errGoogleEventCancelled", err)
	}
}

// --- PushEvent ---------------------------------------------------------------

type capturedReq struct {
	method  string
	path    string
	ifMatch string
	body    googleEvent
	bodyRaw string
}

func newTestServer(t *testing.T, handler func(req capturedReq, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var parsed googleEvent
		if len(bodyBytes) > 0 {
			_ = json.Unmarshal(bodyBytes, &parsed)
		}
		handler(capturedReq{
			method:  r.Method,
			path:    r.URL.Path,
			ifMatch: r.Header.Get("If-Match"),
			body:    parsed,
			bodyRaw: string(bodyBytes),
		}, w)
	}))
}

// helper: build a minimal ICS blob for an event with a known UID.
func minimalICSBlob(t *testing.T, uid string) string {
	t.Helper()
	blob, err := serializeVEVENT(uid, EventInput{
		Summary:     "Test",
		DTStartUnix: time.Date(2026, 6, 10, 14, 0, 0, 0, time.UTC).Unix(),
		DTEndUnix:   time.Date(2026, 6, 10, 15, 0, 0, 0, time.UTC).Unix(),
	})
	if err != nil {
		t.Fatalf("serializeVEVENT: %v", err)
	}
	return blob
}

func TestGoogleProvider_PushEvent_CreateSuccess(t *testing.T) {
	var got capturedReq
	srv := newTestServer(t, func(req capturedReq, w http.ResponseWriter) {
		got = req
		_ = json.NewEncoder(w).Encode(googleEvent{
			ID:      "server-event-id",
			ICalUID: req.body.ICalUID,
			ETag:    `"server-etag-1"`,
			Status:  "confirmed",
			Summary: req.body.Summary,
			Start:   req.body.Start,
			End:     req.body.End,
		})
	})
	defer srv.Close()

	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	ev := Event{UID: "evt@daak-google", ICSBlob: minimalICSBlob(t, "evt@daak-google")}

	result, err := p.PushEvent(t.Context(), src, cal, ev)
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if result.ProviderEventID != "server-event-id" {
		t.Errorf("ProviderEventID = %q, want server-event-id", result.ProviderEventID)
	}
	if result.ETag != `"server-etag-1"` {
		t.Errorf("ETag = %q", result.ETag)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if !strings.HasSuffix(got.path, "/calendars/primary/events") {
		t.Errorf("path = %q, want suffix /calendars/primary/events", got.path)
	}
	if got.ifMatch != "" {
		t.Errorf("If-Match should be empty on create, got %q", got.ifMatch)
	}
	if got.body.ICalUID != "evt@daak-google" {
		t.Errorf("payload iCalUID = %q", got.body.ICalUID)
	}
}

func TestGoogleProvider_PushEvent_UpdatePATCHWithIfMatch(t *testing.T) {
	var got capturedReq
	srv := newTestServer(t, func(req capturedReq, w http.ResponseWriter) {
		got = req
		_ = json.NewEncoder(w).Encode(googleEvent{
			ID:      "existing-id",
			ICalUID: req.body.ICalUID,
			ETag:    `"server-etag-2"`,
			Status:  "confirmed",
		})
	})
	defer srv.Close()

	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	ev := Event{
		UID:             "evt@daak-google",
		ProviderEventID: "existing-id",
		ETag:            `"old-etag"`,
		ICSBlob:         minimalICSBlob(t, "evt@daak-google"),
	}

	result, err := p.PushEvent(t.Context(), src, cal, ev)
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if got.method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", got.method)
	}
	if got.ifMatch != `"old-etag"` {
		t.Errorf("If-Match = %q, want \"old-etag\"", got.ifMatch)
	}
	if !strings.HasSuffix(got.path, "/calendars/primary/events/existing-id") {
		t.Errorf("path = %q", got.path)
	}
	if result.ETag != `"server-etag-2"` {
		t.Errorf("ETag = %q", result.ETag)
	}
}

func TestGoogleProvider_PushEvent_412Conflict(t *testing.T) {
	srv := newTestServer(t, func(_ capturedReq, w http.ResponseWriter) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	defer srv.Close()

	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	ev := Event{
		UID:             "evt@daak-google",
		ProviderEventID: "stale-id",
		ETag:            `"stale-etag"`,
		ICSBlob:         minimalICSBlob(t, "evt@daak-google"),
	}
	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// --- DeleteRemote ------------------------------------------------------------

func TestGoogleProvider_DeleteRemote_Success(t *testing.T) {
	var got capturedReq
	srv := newTestServer(t, func(req capturedReq, w http.ResponseWriter) {
		got = req
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	ev := Event{ProviderEventID: "abc-123", ETag: `"current"`}

	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if got.ifMatch != `"current"` {
		t.Errorf("If-Match = %q", got.ifMatch)
	}
	if !strings.HasSuffix(got.path, "/events/abc-123") {
		t.Errorf("path = %q", got.path)
	}
}

func TestGoogleProvider_DeleteRemote_404Idempotent(t *testing.T) {
	srv := newTestServer(t, func(_ capturedReq, w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()
	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	// ETag set so the empty-ETag re-fetch gate does not run: this test
	// verifies only that a 404 on the DELETE itself is idempotent. The
	// empty-ETag + already-gone path is covered by
	// TestGoogleProvider_DeleteRemote_EmptyETag_GoneServerCopyProceeds.
	ev := Event{ProviderEventID: "missing", ETag: `"current"`}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Errorf("DeleteRemote on 404 should be idempotent, got %v", err)
	}
}

func TestGoogleProvider_DeleteRemote_412Conflict(t *testing.T) {
	srv := newTestServer(t, func(_ capturedReq, w http.ResponseWriter) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	defer srv.Close()
	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	ev := Event{ProviderEventID: "abc", ETag: `"stale"`}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestGoogleProvider_DeleteRemote_NoProviderEventID(t *testing.T) {
	// If the event was never on the server (ProviderEventID empty),
	// DeleteRemote is a no-op so local delete proceeds.
	called := false
	srv := newTestServer(t, func(_ capturedReq, _ http.ResponseWriter) {
		called = true
	})
	defer srv.Close()
	p := newTestGoogleProvider(srv.URL)
	src := Source{ID: "src-g1", Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: "cal-g1", URL: "primary"}
	if err := p.DeleteRemote(t.Context(), src, cal, Event{}); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if called {
		t.Errorf("DeleteRemote should not contact server when ProviderEventID is empty")
	}
}

// Google transparency ⇄ iCal TRANSP (2-state). "transparent" → TRANSPARENT and
// back to "transparent"; "opaque"/"" → busy/opaque.
func TestGoogleTranslate_Transparency(t *testing.T) {
	mk := func(transp string) googleEvent {
		return googleEvent{
			ICalUID: "g-transp@daak-google", Status: "confirmed", Summary: "s",
			Transparency: transp,
			Start:        &googleTimePoint{DateTime: "2026-06-10T14:00:00Z", TimeZone: "UTC"},
			End:          &googleTimePoint{DateTime: "2026-06-10T15:00:00Z", TimeZone: "UTC"},
		}
	}

	freeBlob, err := translateGoogleEventToICS(mk("transparent"))
	if err != nil {
		t.Fatalf("translate transparent: %v", err)
	}
	if !strings.Contains(freeBlob, "TRANSP:TRANSPARENT") {
		t.Errorf("transparency=transparent should map to TRANSP:TRANSPARENT:\n%s", freeBlob)
	}
	if g, _ := translateICSToGoogleJSON(freeBlob); g.Transparency != "transparent" {
		t.Errorf("free ICS → transparency %q, want transparent", g.Transparency)
	}

	busyBlob, err := translateGoogleEventToICS(mk("opaque"))
	if err != nil {
		t.Fatalf("translate opaque: %v", err)
	}
	if strings.Contains(busyBlob, "TRANSP:TRANSPARENT") {
		t.Errorf("transparency=opaque should not be TRANSPARENT:\n%s", busyBlob)
	}
	if g, _ := translateICSToGoogleJSON(busyBlob); g.Transparency != "opaque" {
		t.Errorf("busy ICS → transparency %q, want opaque", g.Transparency)
	}
}

// Google visibility ⇄ iCal CLASS (3-state). public maps to "default" (inherit).
func TestGoogleTranslate_Visibility(t *testing.T) {
	mk := func(vis string) googleEvent {
		return googleEvent{
			ICalUID: "g@daak", Status: "confirmed", Summary: "s", Visibility: vis,
			Start: &googleTimePoint{DateTime: "2026-06-10T14:00:00Z", TimeZone: "UTC"},
			End:   &googleTimePoint{DateTime: "2026-06-10T15:00:00Z", TimeZone: "UTC"},
		}
	}
	cases := []struct{ vis, wantClass, wantVis string }{
		{"default", "", "default"},
		{"private", "CLASS:PRIVATE", "private"},
		{"confidential", "CLASS:CONFIDENTIAL", "confidential"},
	}
	for _, c := range cases {
		blob, err := translateGoogleEventToICS(mk(c.vis))
		if err != nil {
			t.Fatalf("translate %s: %v", c.vis, err)
		}
		if c.wantClass == "" && strings.Contains(blob, "CLASS:") {
			t.Errorf("%s should omit CLASS:\n%s", c.vis, blob)
		}
		if c.wantClass != "" && !strings.Contains(blob, c.wantClass) {
			t.Errorf("%s → %s missing:\n%s", c.vis, c.wantClass, blob)
		}
		if g, _ := translateICSToGoogleJSON(blob); g.Visibility != c.wantVis {
			t.Errorf("%s round-trip visibility = %q, want %q", c.vis, g.Visibility, c.wantVis)
		}
	}
}

func TestGoogleConferenceInfo(t *testing.T) {
	t.Run("conferenceData video + phone", func(t *testing.T) {
		ev := googleEvent{
			ConferenceData: &googleConferenceData{
				ConferenceSolution: &googleConferenceSolution{Name: "Google Meet"},
				EntryPoints: []googleEntryPoint{
					{EntryPointType: "video", URI: "https://meet.google.com/abc-defg-hij"},
					{EntryPointType: "phone", URI: "tel:+1-650-555-0100", Label: "+1 650-555-0100", Pin: "123456789"},
					{EntryPointType: "more", URI: "https://tel.meet/abc-defg-hij"},
				},
			},
		}
		text, uri := googleConferenceInfo(ev)
		if uri != "https://meet.google.com/abc-defg-hij" {
			t.Fatalf("videoURI = %q", uri)
		}
		want := "Google Meet\nhttps://meet.google.com/abc-defg-hij\n\nJoin by phone: +1 650-555-0100 (PIN: 123456789#)"
		if text != want {
			t.Errorf("text = %q, want %q", text, want)
		}
	})

	t.Run("hangoutLink fallback", func(t *testing.T) {
		text, uri := googleConferenceInfo(googleEvent{HangoutLink: "https://meet.google.com/xyz"})
		if uri != "https://meet.google.com/xyz" || text != "Video call\nhttps://meet.google.com/xyz" {
			t.Errorf("text=%q uri=%q", text, uri)
		}
	})

	t.Run("no conferencing", func(t *testing.T) {
		if text, uri := googleConferenceInfo(googleEvent{}); text != "" || uri != "" {
			t.Errorf("expected empty, got text=%q uri=%q", text, uri)
		}
	})
}

func TestConferenceBlockHTML(t *testing.T) {
	text := "Google Meet\nhttps://meet.google.com/abc-defg-hij\n\nJoin by phone: +1 650-555-0100 (PIN: 123456789#)"
	got := conferenceBlockHTML(text, "https://meet.google.com/abc-defg-hij")
	want := "Google Meet<br><a href=\"https://meet.google.com/abc-defg-hij\">https://meet.google.com/abc-defg-hij</a><br><br>Join by phone: +1 650-555-0100 (PIN: 123456789#)"
	if got != want {
		t.Errorf("conferenceBlockHTML =\n%q\nwant\n%q", got, want)
	}
}

// --- R7: empty local ETag ---------------------------------------------------
//
// A Google update/delete with no known local ETag must re-fetch the server
// copy (single-event GET) and compare it with the locally stored blob
// before writing unconditionally — and refuse with ErrConflict when the
// server copy moved, was cancelled, or cannot be verified.

// seedGoogleStoredEvent stores a Google event as of the last sync (empty
// local ETag — the R7 scenario) and returns the source + calendar to call
// the provider with, the server-side event JSON, and the stored ICS blob.
func seedGoogleStoredEvent(t *testing.T, store *Store) (Source, Calendar, googleEvent, string) {
	t.Helper()
	base := googleEvent{
		ID:          "evt-g1",
		ICalUID:     "evt-uid-1@daak-google",
		Status:      "confirmed",
		Summary:     "Project sync",
		Description: "Weekly project status",
		Location:    "Room 4B",
		Start: &googleTimePoint{
			DateTime: "2026-06-10T14:00:00-07:00",
			TimeZone: "America/Los_Angeles",
		},
		End: &googleTimePoint{
			DateTime: "2026-06-10T15:00:00-07:00",
			TimeZone: "America/Los_Angeles",
		},
	}
	blob, err := translateGoogleEventToICS(base)
	if err != nil {
		t.Fatalf("translateGoogleEventToICS: %v", err)
	}
	srcID, calID := "src-g-r7", "cal-g-r7"
	now := time.Now().Unix()
	if err := store.WithTx(func(tx *sql.Tx) error {
		if err := store.CreateSourceTx(tx, Source{
			ID: srcID, Type: SourceTypeGoogle, Name: "Test",
			Enabled: true, Writable: true, CreatedAt: now,
		}); err != nil {
			return err
		}
		return store.CreateCalendarTx(tx, Calendar{
			ID: calID, SourceID: srcID, URL: "primary",
			DisplayName: "Primary", Visible: true, CreatedAt: now,
		})
	}); err != nil {
		t.Fatalf("seed source/calendar: %v", err)
	}
	parsed, err := ParseCalendarObject(blob)
	if err != nil {
		t.Fatalf("parse seed ICS: %v", err)
	}
	stored := parsed.Master
	stored.ID = "ev-g1"
	stored.CalendarID = calID
	stored.ETag = ""
	stored.ProviderEventID = "evt-g1"
	stored.ICSBlob = blob
	if err := store.WithTx(func(tx *sql.Tx) error {
		return store.UpsertEventTx(tx, stored)
	}); err != nil {
		t.Fatalf("seed stored event: %v", err)
	}
	src := Source{ID: srcID, Type: SourceTypeGoogle, AccountID: "acct-1"}
	cal := Calendar{ID: calID, URL: "primary"}
	return src, cal, base, blob
}

// googleR7Server serves a configurable server copy on GET and records every
// request so tests can prove whether a PATCH/DELETE happened.
type googleR7Server struct {
	srv       *httptest.Server
	requests  []capturedReq
	getEvent  googleEvent
	getStatus int // 0 = 200
}

func newGoogleR7Server(t *testing.T, getEvent googleEvent, getStatus int) *googleR7Server {
	t.Helper()
	gs := &googleR7Server{getEvent: getEvent, getStatus: getStatus}
	gs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var parsed googleEvent
		if len(bodyBytes) > 0 {
			_ = json.Unmarshal(bodyBytes, &parsed)
		}
		gs.requests = append(gs.requests, capturedReq{
			method:  r.Method,
			path:    r.URL.Path,
			ifMatch: r.Header.Get("If-Match"),
			body:    parsed,
			bodyRaw: string(bodyBytes),
		})
		switch r.Method {
		case http.MethodGet:
			if gs.getStatus != 0 {
				w.WriteHeader(gs.getStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(gs.getEvent)
		case http.MethodPatch:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(googleEvent{
				ID: "evt-g1", ICalUID: "evt-uid-1@daak-google", ETag: `"new-etag"`,
			})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return gs
}

func (gs *googleR7Server) countMethod(method string) int {
	n := 0
	for _, req := range gs.requests {
		if req.method == method {
			n++
		}
	}
	return n
}

func TestGoogleProvider_PushEvent_EmptyETag_UnchangedServerCopyProceeds(t *testing.T) {
	store := newTestStore(t)
	src, cal, base, blob := seedGoogleStoredEvent(t, store)
	gs := newGoogleR7Server(t, base, 0)
	defer gs.srv.Close()
	p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

	ev := Event{
		UID:             base.ICalUID,
		ProviderEventID: "evt-g1",
		ETag:            "",
		ICSBlob:         strings.Replace(blob, "SUMMARY:Project sync", "SUMMARY:Project sync (my edit)", 1),
	}
	result, err := p.PushEvent(t.Context(), src, cal, ev)
	if err != nil {
		t.Fatalf("PushEvent: %v", err)
	}
	if result.ETag != `"new-etag"` {
		t.Errorf("ETag = %q, want %q", result.ETag, `"new-etag"`)
	}
	if got := gs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1 (re-fetch before unconditional PATCH); requests=%+v", got, gs.requests)
	}
	if got := gs.countMethod(http.MethodPatch); got != 1 {
		t.Fatalf("server saw %d PATCHes, want 1; requests=%+v", got, gs.requests)
	}
	for _, req := range gs.requests {
		if req.method == http.MethodPatch && req.ifMatch != "" {
			t.Errorf("PATCH carried If-Match %q, want none (no local ETag)", req.ifMatch)
		}
	}
}

func TestGoogleProvider_PushEvent_EmptyETag_ChangedServerCopyIsConflict(t *testing.T) {
	store := newTestStore(t)
	src, cal, base, blob := seedGoogleStoredEvent(t, store)
	changed := base
	changed.Summary = "Project sync (moved by someone else)"
	gs := newGoogleR7Server(t, changed, 0)
	defer gs.srv.Close()
	p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

	ev := Event{
		UID:             base.ICalUID,
		ProviderEventID: "evt-g1",
		ETag:            "",
		ICSBlob:         strings.Replace(blob, "SUMMARY:Project sync", "SUMMARY:Project sync (my edit)", 1),
	}
	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict", err)
	}
	if got := gs.countMethod(http.MethodPatch); got != 0 {
		t.Fatalf("server saw %d PATCHes, want 0 (changed copy must not be overwritten blindly); requests=%+v", got, gs.requests)
	}
}

func TestGoogleProvider_PushEvent_EmptyETag_CancelledServerCopyIsConflict(t *testing.T) {
	store := newTestStore(t)
	src, cal, base, blob := seedGoogleStoredEvent(t, store)
	cancelled := base
	cancelled.Status = "cancelled"
	gs := newGoogleR7Server(t, cancelled, 0)
	defer gs.srv.Close()
	p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

	ev := Event{
		UID:             base.ICalUID,
		ProviderEventID: "evt-g1",
		ETag:            "",
		ICSBlob:         strings.Replace(blob, "SUMMARY:Project sync", "SUMMARY:Project sync (my edit)", 1),
	}
	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict (must not resurrect a cancelled event)", err)
	}
	if got := gs.countMethod(http.MethodPatch); got != 0 {
		t.Fatalf("server saw %d PATCHes, want 0; requests=%+v", got, gs.requests)
	}
}

func TestGoogleProvider_PushEvent_EmptyETag_GetFailsIsConflict(t *testing.T) {
	store := newTestStore(t)
	src, cal, base, blob := seedGoogleStoredEvent(t, store)
	gs := newGoogleR7Server(t, base, http.StatusInternalServerError)
	defer gs.srv.Close()
	p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

	ev := Event{
		UID:             base.ICalUID,
		ProviderEventID: "evt-g1",
		ETag:            "",
		ICSBlob:         strings.Replace(blob, "SUMMARY:Project sync", "SUMMARY:Project sync (my edit)", 1),
	}
	_, err := p.PushEvent(t.Context(), src, cal, ev)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("PushEvent err = %v, want ErrConflict", err)
	}
	if got := gs.countMethod(http.MethodPatch); got != 0 {
		t.Fatalf("server saw %d PATCHes, want 0 (unverifiable copy must not be overwritten); requests=%+v", got, gs.requests)
	}
}

func TestGoogleProvider_DeleteRemote_EmptyETag_UnchangedProceeds(t *testing.T) {
	store := newTestStore(t)
	src, cal, base, _ := seedGoogleStoredEvent(t, store)
	gs := newGoogleR7Server(t, base, 0)
	defer gs.srv.Close()
	p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

	ev := Event{UID: base.ICalUID, ProviderEventID: "evt-g1", ETag: ""}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if got := gs.countMethod(http.MethodGet); got != 1 {
		t.Fatalf("server saw %d GETs, want 1; requests=%+v", got, gs.requests)
	}
	if got := gs.countMethod(http.MethodDelete); got != 1 {
		t.Fatalf("server saw %d DELETEs, want 1; requests=%+v", got, gs.requests)
	}
}

func TestGoogleProvider_DeleteRemote_EmptyETag_GoneServerCopyProceeds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		getEvent  googleEvent
		getStatus int
	}{
		{"cancelled tombstone", googleEvent{ID: "evt-g1", ICalUID: "evt-uid-1@daak-google", Status: "cancelled"}, 0},
		{"GET 404", googleEvent{}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			src, cal, base, _ := seedGoogleStoredEvent(t, store)
			gs := newGoogleR7Server(t, tc.getEvent, tc.getStatus)
			defer gs.srv.Close()
			p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

			ev := Event{UID: base.ICalUID, ProviderEventID: "evt-g1", ETag: ""}
			if err := p.DeleteRemote(t.Context(), src, cal, ev); err != nil {
				t.Fatalf("DeleteRemote: %v", err)
			}
			if got := gs.countMethod(http.MethodDelete); got != 1 {
				t.Fatalf("server saw %d DELETEs, want 1 (already-gone copy is a no-op delete); requests=%+v", got, gs.requests)
			}
		})
	}
}

func TestGoogleProvider_DeleteRemote_EmptyETag_ChangedIsConflict(t *testing.T) {
	store := newTestStore(t)
	src, cal, base, _ := seedGoogleStoredEvent(t, store)
	changed := base
	changed.Summary = "Project sync (moved by someone else)"
	gs := newGoogleR7Server(t, changed, 0)
	defer gs.srv.Close()
	p := googleProvider{store: store, auth: fakeAuth{target: gs.srv.URL}}

	ev := Event{UID: base.ICalUID, ProviderEventID: "evt-g1", ETag: ""}
	if err := p.DeleteRemote(t.Context(), src, cal, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteRemote err = %v, want ErrConflict", err)
	}
	if got := gs.countMethod(http.MethodDelete); got != 0 {
		t.Fatalf("server saw %d DELETEs, want 0; requests=%+v", got, gs.requests)
	}
}

// Audit4 finding 17: single-occurrence edits/deletes made elsewhere must
// show up in Daak — overrides become RECURRENCE-ID rows, cancelled ones
// become EXDATE on the master.
func TestPersistEventsPage_GoogleOverrides(t *testing.T) {
	store := newTestStore(t)
	_, calID := seedGoogleSource(t, store, "primary")
	p := googleProvider{store: store}
	cal := Calendar{ID: calID, URL: "primary"}

	masterStart := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) // Monday
	occ2 := masterStart.AddDate(0, 0, 7)
	occ3 := masterStart.AddDate(0, 0, 14)
	items := []googleEvent{
		// Override listed BEFORE its master: the two-pass persist must
		// still find the master.
		{
			ID: "override-1", ICalUID: "master-uid@google", Status: "confirmed",
			RecurringEventID: "master-1", Summary: "Project sync (moved)",
			Start:             &googleTimePoint{DateTime: occ2.Add(time.Hour).Format(time.RFC3339)},
			End:               &googleTimePoint{DateTime: occ2.Add(2 * time.Hour).Format(time.RFC3339)},
			OriginalStartTime: &googleTimePoint{DateTime: occ2.Format(time.RFC3339)},
		},
		{
			ID: "override-2", ICalUID: "master-uid@google", Status: "cancelled",
			RecurringEventID:  "master-1",
			Start:             &googleTimePoint{DateTime: occ3.Format(time.RFC3339)},
			End:               &googleTimePoint{DateTime: occ3.Add(time.Hour).Format(time.RFC3339)},
			OriginalStartTime: &googleTimePoint{DateTime: occ3.Format(time.RFC3339)},
		},
		{
			ID: "master-1", ICalUID: "master-uid@google", Status: "confirmed",
			Summary:    "Project sync",
			Start:      &googleTimePoint{DateTime: masterStart.Format(time.RFC3339)},
			End:        &googleTimePoint{DateTime: masterStart.Add(time.Hour).Format(time.RFC3339)},
			Recurrence: []string{"RRULE:FREQ=WEEKLY;COUNT=4"},
		},
	}
	if err := p.persistEventsPage(cal, items); err != nil {
		t.Fatalf("persistEventsPage: %v", err)
	}
	masterID, err := p.lookupEventIDByUID(calID, "master-uid@google")
	if err != nil || masterID == "" {
		t.Fatalf("master not stored: id=%q err=%v", masterID, err)
	}
	overrides, err := store.ListOverrides(masterID)
	if err != nil {
		t.Fatalf("ListOverrides: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("overrides = %d, want 1 (the moved occurrence)", len(overrides))
	}
	if overrides[0].RecurrenceIDUnix != occ2.Unix() {
		t.Errorf("override recurrence-id = %d, want %d (original start)", overrides[0].RecurrenceIDUnix, occ2.Unix())
	}
	if !strings.Contains(overrides[0].ICSBlob, "Project sync (moved)") {
		t.Errorf("override blob does not carry the moved summary: %q", overrides[0].ICSBlob)
	}
	master, err := store.GetEvent(masterID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !strings.Contains(master.ICSBlob, "EXDATE") {
		t.Errorf("master blob has no EXDATE for the cancelled occurrence: %q", master.ICSBlob)
	}
}
