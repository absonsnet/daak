package backend

// Tests for the CalDAV VCALENDAR composition helper. composeVCalendar
// rewrites the master resource's body to apply a per-instance edit; the
// HTTP PUT plumbing lives in PushInstance + caldavPut and is exercised
// by the httptest-based provider tests separately.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
)

// buildSeedMasterICS constructs an ICS blob containing a master VEVENT
// (with the given RRULE) plus zero or more override VEVENTs. Used as
// the input to composeVCalendar in the tests below.
func buildSeedMasterICS(t *testing.T, uid string, rrule string, instanceTimeUnix int64, overrides []int64) string {
	t.Helper()
	in := EventInput{
		Summary:     "Original",
		DTStartUnix: instanceTimeUnix,
		DTEndUnix:   instanceTimeUnix + 3600,
		Recurrence:  &RecurrenceSpec{Freq: rrule},
	}
	blob, err := serializeVEVENT(uid, in)
	if err != nil {
		t.Fatalf("serializeVEVENT: %v", err)
	}
	if len(overrides) == 0 {
		return blob
	}
	// Splice override VEVENTs into the calendar.
	cal, err := decodeVCalendar(blob)
	if err != nil {
		t.Fatalf("decode seed master: %v", err)
	}
	for _, ot := range overrides {
		ov := buildOverrideVEVENT(uid, ot, EventInput{
			Summary:     "Old override",
			DTStartUnix: ot,
			DTEndUnix:   ot + 1800,
		})
		cal.Children = append(cal.Children, ov.Component)
	}
	out, err := encodeICS(cal)
	if err != nil {
		t.Fatalf("encode seed master: %v", err)
	}
	return out
}

func TestComposeVCalendar_ThisUpdate_AppendsOverride(t *testing.T) {
	uid := "evt-x@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()

	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, nil)
	master := Event{UID: uid, ICSBlob: masterBlob}

	composed, err := composeVCalendar(PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpUpdate,
		In: EventInput{
			Summary:     "Modified instance",
			DTStartUnix: instanceTime,
			DTEndUnix:   instanceTime + 3600,
		},
	})
	if err != nil {
		t.Fatalf("composeVCalendar: %v", err)
	}
	if composed.NewSeries != nil {
		t.Errorf("NewSeries should be nil for scope=this")
	}
	if !strings.Contains(composed.MasterBlob, "SUMMARY:Modified instance") {
		t.Errorf("blob missing modified SUMMARY:\n%s", composed.MasterBlob)
	}
	if !strings.Contains(composed.MasterBlob, "RECURRENCE-ID:20260615T090000Z") {
		t.Errorf("blob missing RECURRENCE-ID:\n%s", composed.MasterBlob)
	}
	// Count VEVENT components — should be 2 (master + new override).
	if got := strings.Count(composed.MasterBlob, "BEGIN:VEVENT"); got != 2 {
		t.Errorf("VEVENT count = %d, want 2", got)
	}
}

func TestComposeVCalendar_ThisUpdate_ReplacesExistingOverride(t *testing.T) {
	uid := "evt-x@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	otherOverrideTime := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC).Unix()

	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, []int64{instanceTime, otherOverrideTime})
	master := Event{UID: uid, ICSBlob: masterBlob}

	composed, err := composeVCalendar(PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpUpdate,
		In: EventInput{
			Summary:     "New override text",
			DTStartUnix: instanceTime,
			DTEndUnix:   instanceTime + 3600,
		},
	})
	if err != nil {
		t.Fatalf("composeVCalendar: %v", err)
	}
	if got := strings.Count(composed.MasterBlob, "BEGIN:VEVENT"); got != 3 {
		t.Errorf("VEVENT count = %d, want 3 (master + 2 overrides)", got)
	}
	// The override matching the targeted time has the new SUMMARY; the
	// other override is unchanged.
	if !strings.Contains(composed.MasterBlob, "SUMMARY:New override text") {
		t.Errorf("blob missing new override SUMMARY")
	}
	if !strings.Contains(composed.MasterBlob, "SUMMARY:Old override") {
		t.Errorf("blob missing untouched override SUMMARY")
	}
}

func TestComposeVCalendar_ThisDelete_AddsEXDATE(t *testing.T) {
	uid := "evt-y@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC).Unix()

	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, nil)
	master := Event{UID: uid, ICSBlob: masterBlob}

	composed, err := composeVCalendar(PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpDelete,
	})
	if err != nil {
		t.Fatalf("composeVCalendar: %v", err)
	}
	if !strings.Contains(composed.MasterBlob, "EXDATE:20260622T090000Z") {
		t.Errorf("blob missing EXDATE:\n%s", composed.MasterBlob)
	}
	if got := strings.Count(composed.MasterBlob, "BEGIN:VEVENT"); got != 1 {
		t.Errorf("VEVENT count = %d, want 1 (master only)", got)
	}
}

func TestComposeVCalendar_ThisDelete_DropsMatchingOverride(t *testing.T) {
	uid := "evt-z@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	otherOverrideTime := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC).Unix()

	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, []int64{instanceTime, otherOverrideTime})
	master := Event{UID: uid, ICSBlob: masterBlob}

	composed, err := composeVCalendar(PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpDelete,
	})
	if err != nil {
		t.Fatalf("composeVCalendar: %v", err)
	}
	// Master + the OTHER override (the matching one was dropped).
	if got := strings.Count(composed.MasterBlob, "BEGIN:VEVENT"); got != 2 {
		t.Errorf("VEVENT count = %d, want 2 (master + untouched override)", got)
	}
	if !strings.Contains(composed.MasterBlob, "EXDATE:20260615T090000Z") {
		t.Errorf("blob missing EXDATE:\n%s", composed.MasterBlob)
	}
}

func TestComposeVCalendar_ThisAndFutureUpdate_ClampsMasterAndCreatesNewSeries(t *testing.T) {
	uid := "evt-tf@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	splitTime := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC).Unix()
	pastOverrideTime := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC).Unix()
	futureOverrideTime := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC).Unix()

	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, []int64{pastOverrideTime, futureOverrideTime})
	master := Event{UID: uid, RRuleText: "FREQ=WEEKLY", ICSBlob: masterBlob}

	composed, err := composeVCalendar(PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: splitTime,
		Op:               EditScopeThisAndFuture,
		Kind:             InstanceOpUpdate,
		In: EventInput{
			Summary:     "New series title",
			DTStartUnix: splitTime,
			DTEndUnix:   splitTime + 3600,
			Recurrence:  &RecurrenceSpec{Freq: "WEEKLY"},
		},
	})
	if err != nil {
		t.Fatalf("composeVCalendar: %v", err)
	}

	// Master: clamped RRULE + past override kept + future override dropped.
	if !strings.Contains(composed.MasterBlob, "UNTIL=") {
		t.Errorf("master missing UNTIL clamp:\n%s", composed.MasterBlob)
	}
	if got := strings.Count(composed.MasterBlob, "BEGIN:VEVENT"); got != 2 {
		t.Errorf("master VEVENT count = %d, want 2 (master + past override)", got)
	}
	if !strings.Contains(composed.MasterBlob, "SUMMARY:Old override") {
		t.Errorf("master missing the past override SUMMARY")
	}

	// New series: separate VCALENDAR with the new fields + new UID.
	if composed.NewSeries == nil {
		t.Fatalf("NewSeries should be set for this-and-future + update")
	}
	if composed.NewSeries.UID == "" || composed.NewSeries.UID == uid {
		t.Errorf("NewSeries UID should be fresh; got %q", composed.NewSeries.UID)
	}
	if !strings.Contains(composed.NewSeries.ICSBlob, "SUMMARY:New series title") {
		t.Errorf("new series blob missing SUMMARY")
	}
	if !strings.Contains(composed.NewSeries.ICSBlob, "FREQ=WEEKLY") {
		t.Errorf("new series blob missing RRULE")
	}
}

func TestComposeVCalendar_ThisAndFutureDelete_ClampsMasterOnly(t *testing.T) {
	uid := "evt-tfd@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	splitTime := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC).Unix()

	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, nil)
	master := Event{UID: uid, RRuleText: "FREQ=WEEKLY", ICSBlob: masterBlob}

	composed, err := composeVCalendar(PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: splitTime,
		Op:               EditScopeThisAndFuture,
		Kind:             InstanceOpDelete,
	})
	if err != nil {
		t.Fatalf("composeVCalendar: %v", err)
	}
	if composed.NewSeries != nil {
		t.Errorf("NewSeries should be nil for delete")
	}
	if !strings.Contains(composed.MasterBlob, "UNTIL=") {
		t.Errorf("master missing UNTIL clamp")
	}
}

func TestRecurrenceIDMatches_RoundTrip(t *testing.T) {
	instanceTime := time.Date(2026, 7, 4, 14, 30, 0, 0, time.UTC).Unix()
	in := EventInput{
		Summary:     "Test",
		DTStartUnix: instanceTime,
		DTEndUnix:   instanceTime + 1800,
	}
	ev := buildOverrideVEVENT("uid@a", instanceTime, in)
	ridProp := ev.Props.Get("RECURRENCE-ID")
	if ridProp == nil {
		t.Fatalf("override missing RECURRENCE-ID")
	}
	if !recurrenceIDMatches(ridProp, instanceTime) {
		t.Errorf("recurrenceIDMatches should match the originating time")
	}
	if recurrenceIDMatches(ridProp, instanceTime+1) {
		t.Errorf("recurrenceIDMatches should NOT match a different time")
	}
}

// --- PushInstance HTTP plumbing (R17) ---------------------------------------
//
// PushInstance used to build its HTTP client from the stored password only,
// so a single-occurrence edit/delete on an OAuth-linked source (which has
// no stored password) failed before any request with "no password stored
// for source — re-add it in settings". It now routes through davClient like
// the other CalDAV write paths. These tests drive PushInstance against an
// httptest server: the OAuth tests use an empty stored password, so they
// fail outright on the old code path and only pass when the Bearer client
// from the auth broker is used.

// staticBearerTransport injects a static Bearer token — the stand-in for
// the auth broker's OAuth-derived client on an OAuth-linked source.
type staticBearerTransport struct{ token string }

func (t staticBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(cloned)
}

// fakeBearerAuth implements coreapi.Auth by returning a client whose
// transport injects a static Bearer token. All other methods are unused
// stubs.
type fakeBearerAuth struct{ token string }

func (f fakeBearerAuth) HTTPClient(_ string, _ []coreapi.AuthScope) (*http.Client, error) {
	return &http.Client{Transport: staticBearerTransport{token: f.token}}, nil
}

func (fakeBearerAuth) IMAPClient(_ string, _ []string) (coreapi.IMAPClient, error) {
	return nil, errors.New("fakeBearerAuth: IMAPClient not implemented")
}

func (fakeBearerAuth) SMTPClient(_ string) (coreapi.SMTPClient, error) {
	return nil, errors.New("fakeBearerAuth: SMTPClient not implemented")
}

func (fakeBearerAuth) StartIncrementalConsent(_ coreapi.StartIncrementalConsentRequest) error {
	return errors.New("fakeBearerAuth: StartIncrementalConsent not implemented")
}

var _ coreapi.Auth = fakeBearerAuth{}

// newPushInstanceServer returns an httptest server that records the
// Authorization header + request of the PUT it receives and answers 200
// with the given ETag.
func newPushInstanceServer(t *testing.T, etag string, gotAuth *string, gotReq *recordedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		*gotAuth = r.Header.Get("Authorization")
		*gotReq = recordedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			ifMatch:     r.Header.Get("If-Match"),
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCalDAVProvider_PushInstance_OAuthLinkedSourceUsesBearerClient(t *testing.T) {
	uid := "evt-oauth@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, nil)

	var gotAuth string
	var gotReq recordedRequest
	srv := newPushInstanceServer(t, `"new-etag"`, &gotAuth, &gotReq)
	defer srv.Close()

	// OAuth-linked source: AccountID set, NO stored password. The old
	// password-only code failed here before sending any request.
	p := caldavProvider{
		secrets: fakeSecrets{password: ""},
		auth:    fakeBearerAuth{token: "oauth-test-token"},
	}
	src := Source{ID: "src-oauth", Type: SourceTypeCalDAV, AccountID: "acct-1", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	master := Event{UID: uid, Href: "/calendars/user/personal/evt-oauth.ics", ETag: `"old-etag"`, ICSBlob: masterBlob}

	result, err := p.PushInstance(t.Context(), src, cal, PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpUpdate,
		In: EventInput{
			Summary:     "Modified instance",
			DTStartUnix: instanceTime,
			DTEndUnix:   instanceTime + 3600,
		},
	})
	if err != nil {
		t.Fatalf("PushInstance: %v", err)
	}
	if result.MasterNewETag != `"new-etag"` {
		t.Errorf("MasterNewETag = %q, want %q", result.MasterNewETag, `"new-etag"`)
	}
	if gotAuth != "Bearer oauth-test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer oauth-test-token")
	}
	if gotReq.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotReq.method)
	}
	if !strings.HasSuffix(gotReq.path, "/evt-oauth.ics") {
		t.Errorf("path = %q, want suffix /evt-oauth.ics", gotReq.path)
	}
	if gotReq.ifMatch != `"old-etag"` {
		t.Errorf("If-Match = %q, want %q", gotReq.ifMatch, `"old-etag"`)
	}
	if !strings.Contains(gotReq.body, "SUMMARY:Modified instance") {
		t.Errorf("PUT body missing modified SUMMARY:\n%s", gotReq.body)
	}
	if !strings.Contains(gotReq.body, "RECURRENCE-ID:20260615T090000Z") {
		t.Errorf("PUT body missing RECURRENCE-ID:\n%s", gotReq.body)
	}
}

func TestCalDAVProvider_PushInstance_OAuthLinkedSourceDeleteSingleOccurrence(t *testing.T) {
	uid := "evt-oauth-del@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC).Unix()
	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, nil)

	var gotAuth string
	var gotReq recordedRequest
	srv := newPushInstanceServer(t, `"new-etag"`, &gotAuth, &gotReq)
	defer srv.Close()

	p := caldavProvider{
		secrets: fakeSecrets{password: ""},
		auth:    fakeBearerAuth{token: "oauth-test-token"},
	}
	src := Source{ID: "src-oauth", Type: SourceTypeCalDAV, AccountID: "acct-1", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	master := Event{UID: uid, Href: "/calendars/user/personal/evt-oauth-del.ics", ETag: `"old-etag"`, ICSBlob: masterBlob}

	result, err := p.PushInstance(t.Context(), src, cal, PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpDelete,
	})
	if err != nil {
		t.Fatalf("PushInstance: %v", err)
	}
	if result.MasterNewETag != `"new-etag"` {
		t.Errorf("MasterNewETag = %q, want %q", result.MasterNewETag, `"new-etag"`)
	}
	if gotAuth != "Bearer oauth-test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer oauth-test-token")
	}
	if !strings.Contains(gotReq.body, "EXDATE:20260622T090000Z") {
		t.Errorf("PUT body missing EXDATE:\n%s", gotReq.body)
	}
}

func TestCalDAVProvider_PushInstance_BasicSourceStillUsesStoredPassword(t *testing.T) {
	uid := "evt-basic@daak-caldav"
	masterStart := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).Unix()
	instanceTime := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	masterBlob := buildSeedMasterICS(t, uid, "WEEKLY", masterStart, nil)

	var gotReq recordedRequest
	srv := newFakeCalDAVServer(t, func(req recordedRequest, w http.ResponseWriter) {
		gotReq = req
		w.Header().Set("ETag", `"new-etag"`)
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	// Basic source: no AccountID, password from secrets, no auth broker.
	p := caldavProvider{secrets: fakeSecrets{password: "secret"}}
	src := Source{ID: "src-basic", Type: SourceTypeCalDAV, Username: "user", URL: srv.URL}
	cal := Calendar{ID: "cal-1", URL: srv.URL + "/calendars/user/personal/"}
	master := Event{UID: uid, Href: "/calendars/user/personal/evt-basic.ics", ETag: `"old-etag"`, ICSBlob: masterBlob}

	result, err := p.PushInstance(t.Context(), src, cal, PushInstancePayload{
		Master:           master,
		InstanceTimeUnix: instanceTime,
		Op:               EditScopeThis,
		Kind:             InstanceOpUpdate,
		In: EventInput{
			Summary:     "Modified instance",
			DTStartUnix: instanceTime,
			DTEndUnix:   instanceTime + 3600,
		},
	})
	if err != nil {
		t.Fatalf("PushInstance: %v", err)
	}
	if result.MasterNewETag != `"new-etag"` {
		t.Errorf("MasterNewETag = %q, want %q", result.MasterNewETag, `"new-etag"`)
	}
	if gotReq.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotReq.method)
	}
	if !strings.Contains(gotReq.body, "SUMMARY:Modified instance") {
		t.Errorf("PUT body missing modified SUMMARY:\n%s", gotReq.body)
	}
}
