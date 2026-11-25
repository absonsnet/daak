package backend

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/emersion/go-webdav"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
	"github.com/absonsnet/daak/internal/kit/davutil"
)

// caldavProvider — Provider impl for SourceTypeCalDAV.
//
// SyncCalendar lifts the PROPFIND/REPORT/diff/upsert logic from the old
// Syncer.syncCalendar verbatim. PushEvent + DeleteRemote are new for
// Chunk 2: raw *http.Client driven PUT/DELETE with conditional headers,
// because emersion/go-webdav v0.7.0's PutCalendarObject doesn't expose
// If-Match / If-None-Match (library TODO at client.go:399).

type caldavProvider struct {
	store   *Store
	secrets coreapi.Secrets
	events  coreapi.EventBus
	auth    coreapi.Auth
	log     coreapi.Logger // optional; nil = silent
}

const (
	caldavScope  = "caldav"
	caldavReason = "Sync and edit your CalDAV calendar events"
)

func (caldavProvider) Capabilities() Capabilities {
	return Capabilities{
		CanWrite:        true,
		CanDeleteSeries: true,
		CanSetReminders: true,
	}
}

// davClient returns the WebDAV HTTP client for src: a Bearer client reusing the
// linked account's OAuth token when AccountID is set (the auth broker refreshes
// it on 401), else a Basic-auth client from the stored credentials. It owns the
// password fetch so the per-operation methods route through one place and can't
// accidentally rebuild a Basic client that bypasses OAuth.
func (p caldavProvider) davClient(src Source, timeout time.Duration) (webdav.HTTPClient, error) {
	if src.AccountID != "" {
		if p.auth == nil {
			return nil, fmt.Errorf("caldavProvider: no Auth handle (extension built without coreapi.Core)")
		}
		hc, err := p.auth.HTTPClient(src.AccountID, []coreapi.AuthScope{{Resource: caldavScope, Reason: caldavReason}})
		if err != nil {
			return nil, err
		}
		// Layer the XML fixups over the broker's refreshing transport.
		return davutil.NewWebDAVClient(hc.Transport, timeout), nil
	}
	password, err := p.secrets.Get(src.ID)
	if err != nil {
		return nil, fmt.Errorf("load password: %w", err)
	}
	if password == "" {
		return nil, fmt.Errorf("no password stored for source — re-add it in settings")
	}
	return davutil.NewBasicDigestHTTPClient(src.Username, password, timeout), nil
}

// --- Sync (lifted from Syncer.syncCalendar) --------------------------------

func (p caldavProvider) SyncCalendar(ctx context.Context, src Source, cal Calendar) error {
	httpClient, err := p.davClient(src, 60*time.Second)
	if err != nil {
		return err
	}
	// Read events via our own tolerant calendar-query REPORT parse rather
	// than go-webdav's QueryCalendar, which aborts the whole list if any one
	// response has empty/unparseable calendar-data (iCloud's self-referential
	// collection entry triggered exactly that — issue #278).
	//
	// cal.URL is stored as a (usually relative) DAV href; resolve it against
	// the source base before requesting — the same way the write path does
	// (absoluteHref). go-webdav's client used to do this resolution for us.
	eventsURL, err := absoluteHref(src.URL, cal.URL)
	if err != nil {
		return fmt.Errorf("resolve calendar URL for %q: %w", cal.DisplayName, err)
	}
	events, err := queryCalendarEvents(ctx, httpClient, eventsURL)
	if err != nil {
		return fmt.Errorf("query calendar %q: %w", cal.DisplayName, err)
	}

	type serverEntry struct {
		etag   string
		href   string
		parsed *ParsedObject
		rawICS string
	}
	server := make(map[string]serverEntry, len(events))
	for _, e := range events {
		if e.rawICS == "" {
			continue
		}
		parsed, perr := ParseCalendarObject(e.rawICS)
		if perr != nil {
			// Skip a malformed object, keep the rest — but say so: an Outlook
			// invite with a Windows TZID vanished here silently for months.
			if p.log != nil {
				p.log.Warn(fmt.Sprintf("calendar sync: skipping unparseable object %s: %v", e.href, perr))
			}
			continue
		}
		server[parsed.Master.UID] = serverEntry{
			etag:   e.etag,
			href:   e.href,
			parsed: parsed,
			rawICS: e.rawICS,
		}
	}

	localETags, err := p.store.ListEventETags(cal.ID)
	if err != nil {
		return fmt.Errorf("list local etags: %w", err)
	}

	return p.store.WithTx(func(tx *sql.Tx) error {
		// Upsert NEW + CHANGED.
		for uid, srv := range server {
			localETag, exists := localETags[uid]
			if exists && localETag == srv.etag && srv.etag != "" {
				continue
			}

			eventID := uuid.New().String()
			if exists {
				if existing, err := p.lookupEventIDByUID(cal.ID, uid); err == nil && existing != "" {
					eventID = existing
				}
			}

			ev := srv.parsed.Master
			ev.ID = eventID
			ev.CalendarID = cal.ID
			ev.ETag = srv.etag
			ev.Href = srv.href

			if err := p.store.UpsertEventTx(tx, ev); err != nil {
				return err
			}

			// Re-write overrides for this event. Inline DELETE because no
			// store helper exists; safe inside the tx.
			if _, err := tx.Exec(
				`DELETE FROM event_recurrence_overrides WHERE event_id = ?`,
				eventID,
			); err != nil {
				return fmt.Errorf("clear old overrides: %w", err)
			}
			for _, ov := range srv.parsed.Overrides {
				if err := p.store.UpsertOverrideTx(tx, eventID, ov.RecurrenceIDUnix, ov.ICSBlob); err != nil {
					return err
				}
			}

			// Compute VALARM instances for the next 7 days. INSERT OR IGNORE
			// in UpsertAlarmTx makes this idempotent across resyncs.
			now := time.Now()
			alarmWindow := now.Add(7 * 24 * time.Hour)
			instances, expErr := ExpandInRange(ev, srv.parsed.Overrides, now, alarmWindow)
			if expErr != nil {
				return fmt.Errorf("expand for alarms: %w", expErr)
			}
			alarms, aerr := ExtractAlarms(ev, srv.parsed.Overrides, instances)
			if aerr != nil {
				return fmt.Errorf("extract alarms: %w", aerr)
			}
			for _, a := range alarms {
				if err := p.store.UpsertAlarmTx(tx, a); err != nil {
					return err
				}
			}
			// Prune rows the fresh extraction no longer covers (moved/
			// edited event, changed VALARMs, dropped occurrence) —
			// otherwise stale rows keep firing at the old times
			// (fourth-audit finding 32).
			if err := p.store.PruneStaleAlarmsTx(tx, eventID, alarms); err != nil {
				return err
			}
		}

		// Delete events that disappeared from the server.
		for uid := range localETags {
			if _, stillOnServer := server[uid]; stillOnServer {
				continue
			}
			if err := p.store.DeleteEventByUIDTx(tx, cal.ID, uid); err != nil {
				return err
			}
		}

		return p.store.UpdateCalendarCtagTx(tx, cal.ID, "", time.Now().Unix())
	})
}

func (p caldavProvider) lookupEventIDByUID(calendarID, uid string) (string, error) {
	var id string
	err := p.store.DB().QueryRow(
		`SELECT id FROM events WHERE calendar_id = ? AND uid = ?`,
		calendarID, uid,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// --- Write (PUT) -----------------------------------------------------------

// PushEvent writes ev to the server via HTTP PUT. ev.Href is set for
// updates; for creates the caller leaves it empty and we synthesize
// `{cal.URL}/{ev.UID}.ics`. Conditional headers ensure optimistic
// concurrency: If-Match for updates (412 on stale ETag), If-None-Match
// for creates (412 if the resource somehow already exists at our URL).
//
// Why raw *http.Client and not caldav.Client.PutCalendarObject:
// emersion/go-webdav v0.7.0's PutCalendarObject doesn't accept
// conditional headers (library TODO at client.go:399). Using raw
// http.Client + the same webdav.HTTPClientWithBasicAuth wrapper gives
// us auth + conditional headers + ETag-from-response in ~30 LOC. If
// the library adds support later, this method becomes the right place
// to swap.
func (p caldavProvider) PushEvent(ctx context.Context, src Source, cal Calendar, ev Event) (PushResult, error) {
	// xmlfix-wrapped client: PUT responses on some servers (mailbox.org)
	// carry unquoted ETags; without the fix the new ETag fails to parse
	// and the retry-on-412 path breaks. Same builder as sync.
	httpClient, err := p.davClient(src, 30*time.Second)
	if err != nil {
		return PushResult{}, err
	}

	// Discriminate create vs update by whether the caller passed an Href.
	// event_crud.go sets ev.Href ONLY after a successful create, so a
	// caller with empty Href is asking us to create a new resource;
	// non-empty Href is asking us to update an existing one. Using ETag
	// emptiness as the discriminator (the previous approach) breaks when
	// a CalDAV server doesn't return an ETag in its PUT response — the
	// local ETag stays empty after create, and a subsequent update would
	// incorrectly send `If-None-Match: *` and get 412 from the server.
	isCreate := ev.Href == ""
	href := ev.Href
	if isCreate {
		href = joinHref(cal.URL, ev.UID+".ics")
	}
	// Resolve relative paths against the source's base URL. CalDAV servers
	// (Nextcloud in particular) return calendar paths as server-relative
	// in PROPFIND responses, so cal.URL / ev.Href may lack scheme + host.
	// emersion/go-webdav handles this internally for its own methods;
	// raw http.Request needs an absolute URL or it errors with
	// "unsupported protocol scheme".
	href, err = absoluteHref(src.URL, href)
	if err != nil {
		return PushResult{}, fmt.Errorf("resolve href: %w", err)
	}

	// Update with no known local ETag (the server omitted the ETag on an
	// earlier PUT response and the follow-up HEAD also failed, so the
	// local copy never learned one): a bare PUT would overwrite any
	// concurrent edit with no 412 ever possible. Re-fetch the server copy
	// first and write unconditionally ONLY when it is proven identical to
	// the locally stored version; otherwise refuse with ErrConflict so
	// the UI shows the conflict instead of silently overwriting.
	if !isCreate && ev.ETag == "" {
		if v := p.serverCopyVerdict(ctx, httpClient, cal, ev, href); v != serverCopyUnchanged {
			return PushResult{}, ErrConflict
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, href, strings.NewReader(ev.ICSBlob))
	if err != nil {
		return PushResult{}, fmt.Errorf("build PUT request: %w", err)
	}
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	// Conditional headers — three cases:
	//   - create (Href empty)                 → If-None-Match: * (reject if exists)
	//   - update with known ETag              → If-Match: ETag    (optimistic concurrency)
	//   - update with no known local ETag     → re-fetch + compare first
	//     (see the gate above); unconditional ONLY when the server copy is
	//     proven unchanged, else ErrConflict — never a bare PUT.
	if isCreate {
		req.Header.Set("If-None-Match", "*")
	}
	if !isCreate && ev.ETag != "" {
		req.Header.Set("If-Match", ev.ETag)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return PushResult{}, fmt.Errorf("caldav PUT: %w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		// RFC 4791 says the response SHOULD carry the new ETag but
		// doesn't require it; Nextcloud often omits it. When absent,
		// HEAD the resource so the local copy has a fresh ETag for the
		// next conditional write — otherwise If-Match on the next edit
		// or delete 412s until the periodic sync catches up.
		etag := resp.Header.Get("ETag")
		if etag == "" {
			etag = fetchETagViaHEAD(ctx, httpClient, href)
		}
		return PushResult{ETag: etag}, nil
	case http.StatusPreconditionFailed:
		// 412 paths split by what we sent:
		//   - Update with If-Match: our ETag is stale. That is often our
		//     own doing (an earlier PUT succeeded without returning an
		//     ETag, so the local copy never learned the new one), but it
		//     can also be someone else's edit to a shared calendar.
		//     Re-fetch the server copy and retry unconditionally ONLY if
		//     it is substantively identical to the locally stored version
		//     — i.e. nobody else changed it. Otherwise surface
		//     ErrConflict so the UI shows the conflict instead of
		//     silently overwriting the other person's edit.
		//   - Create with If-None-Match: * (resource already exists):
		//     genuine conflict, surface ErrConflict so the queue / UI can
		//     handle it.
		if !isCreate && ev.ETag != "" && p.serverCopyVerdict(ctx, httpClient, cal, ev, href) == serverCopyUnchanged {
			return p.retryPutUnconditional(ctx, httpClient, href, ev.ICSBlob)
		}
		return PushResult{}, ErrConflict
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return PushResult{}, fmt.Errorf("caldav PUT %d %s: %s",
		resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
}

// retryPutUnconditional re-sends a PUT without any If-Match / If-None-Match
// header. Called only from the update path after the server-copy verdict
// proved the server copy is substantively identical to the locally stored
// version — i.e. the 412 came from our own stale ETag, not from someone
// else's edit. It must never be called on a bare 412: that would silently
// overwrite a concurrent edit.
func (p caldavProvider) retryPutUnconditional(ctx context.Context, httpClient webdav.HTTPClient, href, blob string) (PushResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, href, strings.NewReader(blob))
	if err != nil {
		return PushResult{}, fmt.Errorf("build retry PUT request: %w", err)
	}
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return PushResult{}, fmt.Errorf("caldav retry PUT: %w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		etag := resp.Header.Get("ETag")
		if etag == "" {
			etag = fetchETagViaHEAD(ctx, httpClient, href)
		}
		return PushResult{ETag: etag}, nil
	case http.StatusPreconditionFailed:
		// Unconditional PUT still rejected — something else is wrong
		// (rare; possibly server-side resource lock). Surface as conflict.
		return PushResult{}, ErrConflict
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return PushResult{}, fmt.Errorf("caldav retry PUT %d %s: %s",
		resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
}

// fetchServerICS GETs the server's current copy of href and returns its raw
// ICS body plus the HTTP status. Any failure (non-200, unreadable body) is
// an error — a copy we cannot read cannot prove anything about the server
// state, so callers must treat it as "not proven unchanged". The status is
// returned alongside so callers can tell "gone" (404/410) apart from a
// transient failure.
func fetchServerICS(ctx context.Context, httpClient webdav.HTTPClient, href string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build GET request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("caldav GET: %w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("caldav GET %d %s", resp.StatusCode, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read GET body: %w", err)
	}
	return string(body), resp.StatusCode, nil
}

// serverCopyVerdict re-fetches the server's current copy of the event and
// compares it with the locally stored version (the stored ICS blob parsed
// the same way). It answers the one question a 412 — or a missing local
// ETag — leaves open: did the server move because of our own stale ETag,
// or because someone else edited the event?
//
// serverCopyUnknown covers every case where the comparison cannot be made:
// no store wired (tests), no local row, no stored ICS blob, a GET that
// fails or an unparseable body. Callers treat unknown as a genuine
// conflict, never as unchanged. A GET that 404s/410s means the server copy
// is gone: serverCopyGone.
func (p caldavProvider) serverCopyVerdict(ctx context.Context, httpClient webdav.HTTPClient, cal Calendar, ev Event, href string) serverCopyVerdict {
	if p.store == nil {
		return serverCopyUnknown
	}
	id, err := p.lookupEventIDByUID(cal.ID, ev.UID)
	if err != nil || id == "" {
		return serverCopyUnknown
	}
	stored, err := p.store.GetEvent(id)
	if err != nil || stored == nil || stored.ICSBlob == "" {
		return serverCopyUnknown
	}
	serverICS, status, err := fetchServerICS(ctx, httpClient, href)
	if err != nil {
		if status == http.StatusNotFound || status == http.StatusGone {
			return serverCopyGone
		}
		return serverCopyUnknown
	}
	server, err := ParseCalendarObject(serverICS)
	if err != nil {
		return serverCopyUnknown
	}
	local, err := ParseCalendarObject(stored.ICSBlob)
	if err != nil {
		return serverCopyUnknown
	}
	if sameCalendarSubstance(server, local) {
		return serverCopyUnchanged
	}
	return serverCopyChanged
}

// compareServerICS parses two ICS blobs and reports whether they hold the
// same calendar substance. Shared by the Google/Microsoft verdict helpers,
// whose server copies arrive as translated ICS. False on any parse failure:
// an unreadable copy cannot prove the server is unchanged.
//
// Note it uses the field-wise comparison only, NOT the canonical
// fingerprint backstop: the JSON→ICS translation is lossy by design
// (an HTML body rides as X-ALT-DESC in a Daak-authored blob but comes back
// as plaintext DESCRIPTION from Google/Graph), so representation artefacts
// that are not concurrent edits would trip the fingerprint. The field
// comparison still covers every item the re-audit names (STATUS via the
// cancelled/gone mapping in the callers, organiser, alarms, exception
// dates, SEQUENCE/categories where the provider models them).
func compareServerICS(serverICS, storedICS string) bool {
	server, err := ParseCalendarObject(serverICS)
	if err != nil {
		return false
	}
	local, err := ParseCalendarObject(storedICS)
	if err != nil {
		return false
	}
	return sameCalendarSubstanceFields(server, local)
}

// sameCalendarSubstance compares two parsed calendar objects on the fields a
// concurrent edit could change. Both sides go through the same parser, so
// formatting differences (line folding, property order) do not count as
// changes. Anything the comparison does not cover is treated as a change by
// the caller: when in doubt, the write is a conflict, never an overwrite.
//
// Compared, field by field: summary, description, HTML description,
// location, times, all-day flag, timezone, recurrence rule, transparency,
// visibility, STATUS, SEQUENCE, categories, exception dates, alarms,
// organiser, and attendees. The canonical fingerprint backstop additionally
// covers every other master-VEVENT property (RDATE, DURATION, X-ALT-DESC,
// vendor X-* props, ...) that the parser does not model, and the override
// blobs are compared whole — a formatting-only change to an override reads
// as a conflict, which errs the safe way.
func sameCalendarSubstance(a, b *ParsedObject) bool {
	if !sameCalendarSubstanceFields(a, b) {
		return false
	}
	// Backstop: any unmodelled master-VEVENT property that differs counts
	// as a change. A fingerprint that cannot be computed proves nothing,
	// so it counts as a change too.
	fa, fb := canonicalMasterFingerprint(a), canonicalMasterFingerprint(b)
	if fa == "" || fb == "" || fa != fb {
		return false
	}
	return true
}

// sameCalendarSubstanceFields is the field-wise half of
// sameCalendarSubstance (everything except the canonical fingerprint
// backstop). The Google/Microsoft verdict helpers use it directly — see
// compareServerICS for why the backstop is skipped there.
func sameCalendarSubstanceFields(a, b *ParsedObject) bool {
	if a == nil || b == nil {
		return false
	}
	am, bm := a.Master, b.Master
	if am.Summary != bm.Summary ||
		am.Description != bm.Description ||
		am.DescriptionHTML != bm.DescriptionHTML ||
		am.Location != bm.Location ||
		am.DTStartUnix != bm.DTStartUnix ||
		am.DTEndUnix != bm.DTEndUnix ||
		am.IsAllDay != bm.IsAllDay ||
		am.TZName != bm.TZName ||
		am.RRuleText != bm.RRuleText ||
		am.Transparency != bm.Transparency ||
		am.Visibility != bm.Visibility ||
		am.Status != bm.Status ||
		am.Sequence != bm.Sequence {
		return false
	}
	if !sameOrganizer(am.Organizer, bm.Organizer) {
		return false
	}
	if !sameStringSet(am.Categories, bm.Categories) {
		return false
	}
	if !sameInt64Set(am.ExceptionDates, bm.ExceptionDates) {
		return false
	}
	if !sameStringSet(am.AlarmFingerprints, bm.AlarmFingerprints) {
		return false
	}
	if !sameAttendees(am.Attendees, bm.Attendees) {
		return false
	}
	if !sameOverrides(a.Overrides, b.Overrides) {
		return false
	}
	return true
}

// sameOrganizer compares organisers by email (case-insensitive) + display
// name. Both absent is equal; one absent is a change — an organiser added
// or removed server-side is a concurrent edit.
func sameOrganizer(a, b *Organizer) bool {
	if a == nil || b == nil {
		return a == b
	}
	return strings.EqualFold(strings.TrimSpace(a.Email), strings.TrimSpace(b.Email)) &&
		a.CommonName == b.CommonName
}

// sameStringSet compares two string slices as sets (order-insensitive).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// sameInt64Set compares two int64 slices as sets (order-insensitive).
func sameInt64Set(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]int64(nil), a...)
	bs := append([]int64(nil), b...)
	sort.Slice(as, func(i, j int) bool { return as[i] < as[j] })
	sort.Slice(bs, func(i, j int) bool { return bs[i] < bs[j] })
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// sameOverrides compares RECURRENCE-ID override lists by instance time +
// whole blob. The blobs are re-encoded by the parser on both sides, so
// formatting differences do not count — but any content change inside an
// override (STATUS, summary, alarm, ...) does.
func sameOverrides(a, b []EventOverride) bool {
	if len(a) != len(b) {
		return false
	}
	ao := append([]EventOverride(nil), a...)
	bo := append([]EventOverride(nil), b...)
	sort.Slice(ao, func(i, j int) bool { return ao[i].RecurrenceIDUnix < ao[j].RecurrenceIDUnix })
	sort.Slice(bo, func(i, j int) bool { return bo[i].RecurrenceIDUnix < bo[j].RecurrenceIDUnix })
	for i := range ao {
		if ao[i].RecurrenceIDUnix != bo[i].RecurrenceIDUnix || ao[i].ICSBlob != bo[i].ICSBlob {
			return false
		}
	}
	return true
}

// sameAttendees compares attendee lists as sets keyed on the fields an edit
// could change. Order in the ICS does not count as a change.
func sameAttendees(a, b []Attendee) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(at Attendee) string {
		return strings.ToLower(strings.TrimSpace(at.Email)) + "|" + at.PartStat + "|" + at.Role + "|" + at.CommonName
	}
	ak := make([]string, len(a))
	bk := make([]string, len(b))
	for i := range a {
		ak[i] = key(a[i])
		bk[i] = key(b[i])
	}
	sort.Strings(ak)
	sort.Strings(bk)
	for i := range ak {
		if ak[i] != bk[i] {
			return false
		}
	}
	return true
}

// fetchETagViaHEAD issues a HEAD against href and returns the ETag header
// value, or "" on any failure / non-2xx / missing header. Called after a
// successful PUT whose response had no ETag — common on Nextcloud, which
// RFC 4791 permits (SHOULD, not MUST) but breaks our If-Match path on the
// next write. One extra round trip per server-that-doesn't-return-ETag-on-PUT;
// servers that DO return ETag inline pay nothing. Failure is non-fatal:
// a later 412 then surfaces as ErrConflict unless a re-fetch proves the
// server copy unchanged.
func fetchETagViaHEAD(ctx context.Context, httpClient webdav.HTTPClient, href string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, href, nil)
	if err != nil {
		return ""
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	return resp.Header.Get("ETag")
}

// --- Delete ---------------------------------------------------------------

// DeleteRemote deletes ev's resource from the server. Honors If-Match
// for optimistic concurrency. With no known local ETag it re-fetches and
// compares the server copy first (same gate as PushEvent) instead of
// sending a bare DELETE. Treats 404 as success (idempotent — if it's
// already gone, the local delete still needs to proceed).
func (p caldavProvider) DeleteRemote(ctx context.Context, src Source, cal Calendar, ev Event) error {
	if ev.Href == "" {
		// No href means it was never on the server (or sync hadn't run).
		// Local delete still proceeds; nothing to do here.
		return nil
	}

	// xmlfix-wrapped client — see PushEvent above for the rationale.
	httpClient, err := p.davClient(src, 30*time.Second)
	if err != nil {
		return err
	}

	href, err := absoluteHref(src.URL, ev.Href)
	if err != nil {
		return fmt.Errorf("resolve href: %w", err)
	}

	// Delete with no known local ETag: a bare DELETE would remove any
	// concurrent edit with no 412 ever possible. Re-fetch first and
	// delete unconditionally ONLY when the server copy is proven identical
	// to the locally stored version — or already gone, in which case the
	// delete is a no-op success. Otherwise refuse with ErrConflict.
	if ev.ETag == "" {
		switch v := p.serverCopyVerdict(ctx, httpClient, cal, ev, href); v {
		case serverCopyUnchanged, serverCopyGone:
		default:
			return ErrConflict
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, href, nil)
	if err != nil {
		return fmt.Errorf("build DELETE request: %w", err)
	}
	if ev.ETag != "" {
		req.Header.Set("If-Match", ev.ETag)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("caldav DELETE: %w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	case http.StatusPreconditionFailed:
		// 412 on a conditional DELETE: our local ETag is stale. That is
		// often our own doing (an earlier PUT left no ETag behind), but it
		// can also be someone else's edit. Re-fetch the server copy and
		// retry unconditionally ONLY if it is substantively identical to
		// the locally stored version; otherwise surface ErrConflict so the
		// UI shows the conflict instead of silently deleting the other
		// person's version.
		if ev.ETag != "" && p.serverCopyVerdict(ctx, httpClient, cal, ev, href) == serverCopyUnchanged {
			return p.retryDeleteUnconditional(ctx, httpClient, href)
		}
		return ErrConflict
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("caldav DELETE %d %s: %s",
		resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
}

// retryDeleteUnconditional re-sends a DELETE without If-Match. Mirror of
// retryPutUnconditional: called only after the server-copy verdict proved
// the server copy is substantively identical to the locally stored version,
// so the 412 came from our own stale ETag rather than a concurrent edit.
func (p caldavProvider) retryDeleteUnconditional(ctx context.Context, httpClient webdav.HTTPClient, href string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, href, nil)
	if err != nil {
		return fmt.Errorf("build retry DELETE request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("caldav retry DELETE: %w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	case http.StatusPreconditionFailed:
		// Unconditional DELETE still rejected — server-side lock or ACL
		// constraint. Surface as conflict.
		return ErrConflict
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("caldav retry DELETE %d %s: %s",
		resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
}

// joinHref appends suffix to base preserving the URL shape. Uses path.Join
// after stripping/restoring any scheme://host prefix so the result keeps
// scheme + host (path.Join would mangle "https://" → "https:/").
func joinHref(base, suffix string) string {
	// Find the path part after scheme://host.
	if idx := strings.Index(base, "://"); idx != -1 {
		rest := base[idx+3:]
		slash := strings.Index(rest, "/")
		if slash == -1 {
			return base + "/" + suffix
		}
		hostPath := base[:idx+3+slash]
		urlPath := rest[slash:]
		return hostPath + path.Join(urlPath, suffix)
	}
	return path.Join(base, suffix)
}

// absoluteHref makes href absolute by resolving it against srcURL (the
// source's base URL) when href is a server-relative path. CalDAV servers
// like Nextcloud return calendar paths as "/remote.php/dav/calendars/..."
// in PROPFIND responses; emersion/go-webdav resolves these internally for
// its own methods, but raw http.Request needs a fully-qualified URL.
//
// Behavior:
//   - href already absolute (has scheme) → returned unchanged.
//   - href relative + srcURL absolute → resolved via url.ResolveReference.
//   - srcURL not parseable as absolute → error (we have nothing to anchor to).
func absoluteHref(srcURL, href string) (string, error) {
	if strings.Contains(href, "://") {
		return href, nil
	}
	base, err := url.Parse(srcURL)
	if err != nil {
		return "", fmt.Errorf("parse source URL %q: %w", srcURL, err)
	}
	if !base.IsAbs() {
		return "", fmt.Errorf("source URL %q is not absolute — cannot resolve %q", srcURL, href)
	}
	rel, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("parse href %q: %w", href, err)
	}
	return base.ResolveReference(rel).String(), nil
}
