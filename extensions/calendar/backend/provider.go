package backend

// Provider abstraction — Phase 2 Chunk 2.
//
// Goal: route sync + write transport per source type (local, caldav, soon
// google, microsoft). The API layer (event_crud.go) calls into a Provider
// for the actual remote PUT/DELETE; Syncer.syncSourceInner dispatches
// per-calendar sync the same way. Read paths (rrule_expand, agenda
// rendering) bypass the provider — they only touch the local DB.
//
// Design choice — same package, not subpackages: the Provider impls need
// Source/Calendar/Event types + Store methods that all live in `backend`.
// A subpackage layout would force lifting the model types into a third
// `model` package or duplicating them, both of which add code without
// real isolation gain. The trade-off is that all Provider files share
// the `backend` package; the file-naming convention (`provider_*.go`)
// keeps them visually grouped.
//
// Design choice — narrow interface (Sync + Push + DeleteRemote, not the
// full CreateEvent/UpdateEvent/DeleteEvent surface): Phase 3's scope-aware
// split logic (updateAll/updateThis/updateThisAndFuture, deleteThis /
// deleteThisAndFuture) is identical across local and CalDAV — both produce
// the same ICS blob; only the transport differs. Keeping the split logic
// at the API layer (where it already lives, well-tested) and giving the
// Provider a single `PushEvent(blob)` method avoids reimplementing the
// scope split per provider. When Google + Microsoft arrive in Chunks 3-4,
// their PushEvent does the ICS-blob-to-native-JSON translation inside the
// provider boundary.

import (
	"context"
	"errors"
	"strings"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
)

// Provider is the per-source-type abstraction. Implementations are NOT
// goroutine-safe by themselves; callers serialize per source via
// Syncer.tryAcquireBusy + user-driven CRUD being sequential.
type Provider interface {
	Capabilities() Capabilities

	// SyncCalendar performs one sync pass for a single calendar. local:
	// no-op. caldav: pulls + diff + upsert via emersion/go-webdav. Future
	// google/microsoft: incremental via syncToken / deltaLink.
	SyncCalendar(ctx context.Context, src Source, cal Calendar) error

	// PushEvent transmits ev (with ev.ICSBlob already serialized by the
	// API layer) to the remote, returning the new server-assigned ETag.
	// ev.ETag == "" signals create (caldav: If-None-Match: *).
	// ev.ETag != "" signals update (caldav: If-Match: ev.ETag).
	// local: no-op, returns zero PushResult, nil error.
	PushEvent(ctx context.Context, src Source, cal Calendar, ev Event) (PushResult, error)

	// DeleteRemote removes ev's resource from the remote. Called only for
	// whole-series delete (EditScopeAll). For scope=this / this-and-future,
	// the API layer builds a modified blob and calls PushInstance instead.
	// local: no-op, returns nil.
	DeleteRemote(ctx context.Context, src Source, cal Calendar, ev Event) error

	// PushInstance applies a scoped edit (EditScopeThis or
	// EditScopeThisAndFuture) to a recurring event. The API layer in
	// event_crud.go has already produced the local-side blobs for these
	// scopes; this method handles the per-provider transport:
	//   - CalDAV: PUT the recomposed VCALENDAR (master + overrides) — and
	//     for this-and-future, PUT a second resource for the new series.
	//   - Google: instances lookup → PATCH/DELETE on the instance event;
	//     for this-and-future, PATCH master with clamped UNTIL + POST the
	//     new series.
	//   - Microsoft: Graph instances endpoint → PATCH/DELETE; for
	//     this-and-future, PATCH master.recurrence.range.endDate + POST new
	//     series.
	//   - local: no-op (no remote — event_crud.go's existing scope branches
	//     handle the local-only side directly).
	//
	// Returns identifiers for the modified override (Google/Microsoft —
	// CalDAV embeds the override in the master resource) and, for
	// this-and-future updates, identifiers for the newly created series.
	PushInstance(ctx context.Context, src Source, cal Calendar, payload PushInstancePayload) (PushInstanceResult, error)
}

// Capabilities describes the provider's static feature set. Frontend
// reads Source.Writable (derived from CanWrite at insert / sync time)
// to gate Edit/Delete affordances.
type Capabilities struct {
	CanWrite        bool
	CanDeleteSeries bool
	CanSetReminders bool
}

// PushResult carries what the API layer persists after a successful
// remote push.
//
//   - ETag: new server-side ETag (caldav, google, microsoft). Empty for local.
//   - ProviderEventID: server-assigned event identifier — Google's eventId,
//     Microsoft Graph's id. Persisted onto events.provider_event_id so
//     subsequent updates can PATCH/DELETE by that ID. Empty for caldav
//     (uses href) and local.
//   - Attendees / Organizer: when non-nil, the server's authoritative
//     attendee list after the push. updateAllAndPush stitches these onto
//     ev so the local row reflects the server's view (e.g., a Google
//     time-change PATCH resets each attendee's responseStatus to
//     needsAction, and we need to surface that locally without waiting
//     for the next sync). CalDAV PUT returns no body, so it leaves them
//     nil and the next sync picks up any server-side changes.
type PushResult struct {
	ETag            string
	ProviderEventID string
	Attendees       []Attendee
	Organizer       *Organizer
}

// InstanceOpKind discriminates update vs delete for per-instance pushes.
type InstanceOpKind string

const (
	InstanceOpUpdate InstanceOpKind = "update"
	InstanceOpDelete InstanceOpKind = "delete"
)

// PushInstancePayload is the input to Provider.PushInstance. It carries
// the master event's current state (needed for ETag + ICSBlob composition)
// plus the targeted occurrence + new fields.
type PushInstancePayload struct {
	Master           Event     // current master with ETag, Href, ProviderEventID, ICSBlob
	InstanceTimeUnix int64     // original DTSTART of the targeted occurrence (unix seconds, UTC)
	Op               EditScope // EditScopeThis | EditScopeThisAndFuture
	Kind             InstanceOpKind
	In               EventInput // new fields for update; ignored for delete
}

// PushInstanceResult carries what the API layer persists after a
// successful per-instance push.
//
//   - MasterNewETag: master resource's new ETag. CalDAV: always set (the
//     whole VCALENDAR was replaced). Google/Microsoft: only set when the
//     master itself was modified (scope=this-and-future clamps it).
//   - OverrideProviderEventID + OverrideETag: Google's exception event
//     resource id / Microsoft's instance event id and ETag. Empty for
//     CalDAV (override lives inside the master resource).
//   - NewSeries: identifiers for the newly created future series.
//     Populated only when Op=EditScopeThisAndFuture and Kind=InstanceOpUpdate.
type PushInstanceResult struct {
	MasterNewETag           string
	OverrideProviderEventID string
	OverrideETag            string
	NewSeries               *NewSeriesIdentifiers
}

// NewSeriesIdentifiers carries the server identifiers for a future
// series created by a this-and-future update.
type NewSeriesIdentifiers struct {
	UID             string
	ETag            string
	Href            string // CalDAV
	ProviderEventID string // Google / Microsoft
}

// Provider errors that callers can switch on.
var (
	// ErrConflict surfaces HTTP 412 Precondition Failed (CalDAV If-Match
	// rejected, Google/MS ETag mismatch). Caller refetches + surfaces a
	// "please re-edit" toast.
	ErrConflict = errors.New("calendar: server-side conflict — refresh and retry")

	// ErrNotWritable is returned by event_crud.go when the source's
	// Writable flag is false. Frontend should have prevented the call
	// reaching here via the isWritable gate; this is a defensive layer.
	ErrNotWritable = errors.New("calendar: source is not writable")

	// ErrScopeNotSupported is reserved for future provider-specific
	// constraints (e.g., RRULE shapes the provider can't represent).
	// Today no provider returns it — per-instance editing works across
	// all four source types after the PushInstance method shipped.
	ErrScopeNotSupported = errors.New("calendar: this scope is not yet supported on this provider")

	// ErrTransport wraps connectivity-layer failures returned from
	// `*http.Client.Do` — DNS, connection refused, timeout, TLS errors —
	// anything that signals the user is offline or the server is
	// unreachable rather than rejecting the request. HTTP-status errors
	// (412 conflict, 5xx) are NOT wrapped with this; they're real server
	// responses that the caller can decide to surface immediately.
	//
	// event_crud.go branches on `errors.Is(err, ErrTransport)` to decide
	// whether to soft-commit locally + enqueue the push for later, or
	// hard-fail the user's save. The provider impls wrap their `client.Do`
	// error path with `fmt.Errorf("%w: %v", ErrTransport, err)` so the
	// sentinel propagates through any wrapping chain.
	ErrTransport = errors.New("calendar: transport-level failure")

	// ErrAuth marks an authentication failure (HTTP 401/403, or a token
	// refresh that could not produce a usable credential). The offline
	// queue must NOT count it toward a row's retry budget (audit4
	// finding 21): the user is not offline and the edit is not broken —
	// the credentials are. The row stays pending for the next Drain
	// after the user re-authenticates.
	ErrAuth = errors.New("calendar: authentication failed — sign in again")
)

// IsAuthFailure reports whether err is an authentication failure: the
// ErrAuth sentinel, or an HTTP 401/403 surfaced by a provider call site.
// Provider write paths format status errors with the status line in
// the message ("google post event 401 401 Unauthorized: ..."), so the
// classifier matches the code together with its status text — a body
// that merely mentions a bare 401 does not match.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAuth) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "401 Unauthorized") || strings.Contains(msg, "403 Forbidden")
}

// serverCopyVerdict is the outcome of re-fetching the server's current
// copy of an event and comparing it with the locally stored version.
// It answers the question an empty local ETag (or a 412) leaves open: did
// the server move because of our own stale/missing ETag, or because
// someone else changed the event?
//
//   - serverCopyUnchanged: server copy is substantively identical to the
//     stored version — an unconditional write is safe.
//   - serverCopyChanged: someone else changed it — refuse, surface
//     ErrConflict.
//   - serverCopyGone: the resource no longer exists server-side. For a
//     PUT that is a change (do not recreate a deleted event); for a
//     DELETE it is already the desired end state, so the delete may
//     proceed.
//   - serverCopyUnknown: the comparison could not be made (no store, no
//     local row, fetch failed, unparseable body) — refuse, surface
//     ErrConflict. Unknown is never treated as unchanged.
type serverCopyVerdict int

const (
	serverCopyUnchanged serverCopyVerdict = iota
	serverCopyChanged
	serverCopyGone
	serverCopyUnknown
)

// ProviderDeps groups the host-supplied dependencies a Provider may need.
// Each impl takes what it needs:
//   - localProvider: nothing.
//   - caldavProvider: secrets (CalDAV password) + store + events; also auth, used
//     only when a source links a custom-OAuth account (Bearer instead of Basic).
//   - googleProvider, microsoftProvider: auth (OAuth-vended *http.Client) +
//     store. The Google calendar scope routes through core's mail slot
//     ("google-mail") — the linked account's grant carries it — while
//     Microsoft calendar uses its own slot ("microsoft-calendar"); the
//     broker routes scope→slot via Auth.HTTPClient.
type ProviderDeps struct {
	Store   *Store
	Secrets coreapi.Secrets
	Events  coreapi.EventBus
	Auth    coreapi.Auth
	// Log is the host's extension-tagged logger (optional; nil = silent). Used
	// for sync diagnostics.
	Log coreapi.Logger
}

// ProviderForSource dispatches src.Type → Provider impl. Falls back to
// localProvider for unknown types so callers never get nil.
func ProviderForSource(src Source, deps ProviderDeps) Provider {
	switch src.Type {
	case SourceTypeCalDAV:
		return caldavProvider{store: deps.Store, secrets: deps.Secrets, events: deps.Events, auth: deps.Auth, log: deps.Log}
	case SourceTypeGoogle:
		return googleProvider{store: deps.Store, auth: deps.Auth}
	case SourceTypeMicrosoft:
		return microsoftProvider{store: deps.Store, auth: deps.Auth, log: deps.Log}
	}
	return localProvider{}
}
