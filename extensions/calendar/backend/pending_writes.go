package backend

// Pending-write queue — Phase 2 Chunk 5.
//
// Persists user-initiated writes that failed at the transport layer (DNS,
// connection refused, timeout — anything errors.Is(err, ErrTransport))
// so they can be retried when connectivity returns. The user's local DB
// commit succeeds immediately, the queued write drains later.
//
// HTTP errors (412 conflict, 5xx) are NOT queued — they're real server
// responses that surface immediately to the user via event_crud.go's
// existing error path.
//
// Drain runs:
//   - inside Syncer.syncSourceInner, after each successful per-source
//     sync (catch-up for stuck rows).
//   - implicitly via the system:network-online + system:wake handlers
//     since those call SyncAllSources → SyncSource → syncSourceInner →
//     Drain.
//
// Order: rows for a given source replay in created_unix ASC order.
//
// Multi-edit collapse (e.g., 3 sequential offline edits to the same
// event) is NOT implemented in Chunk 5 — the queue stores each edit
// separately and replays them in order. Worst case: 1-2 spurious
// conflict toasts; end state matches the latest edit.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
	"github.com/absonsnet/daak/internal/logging"
)

// PendingOpKind enumerates queueable operations.
type PendingOpKind string

const (
	PendingOpCreate PendingOpKind = "create"
	PendingOpUpdate PendingOpKind = "update"
	PendingOpDelete PendingOpKind = "delete"
)

// pendingMaxAttempts is the per-row retry budget. After this many failed
// drain attempts, Drain skips the row (logs but doesn't fail). A future
// chunk can expose retry / clear UI; for Chunk 5 the row just sits there
// awaiting that future surface.
const pendingMaxAttempts = 3

// PendingOp is the input shape to Enqueue. The caller supplies the
// full event-shaped state needed to replay the push (or delete) without
// re-reading the events row at drain time — important because
// soft-committed deletes have no events row to read.
type PendingOp struct {
	SourceID   string
	CalendarID string
	EventID    string // local events.id; empty when no row (rare)
	Op         PendingOpKind
	Scope      EditScope // applies to update/delete; empty for create

	// InstanceTimeUnix + Kind carry a scoped (this / this-and-future)
	// split for Drain: the original occurrence start and whether the
	// split updates or deletes it. Zero/empty for whole-series ops.
	InstanceTimeUnix int64
	Kind             InstanceOpKind
	// In carries the new fields for a scoped update, so Drain can
	// replay the split without re-reading the (un-split) events row.
	In *EventInput

	// CalendarURL holds the URL/ID the provider uses to address the
	// calendar (CalDAV path, Google calendarId, Microsoft Graph id).
	// Copied from Calendar.URL at enqueue time so Drain doesn't need a
	// fresh DB lookup.
	CalendarURL string

	// Event fields needed to construct the replay request. For deletes,
	// only ETag + Href + ProviderEventID are load-bearing; the rest are
	// stored for traceability.
	UID             string
	ETag            string
	Href            string
	ProviderEventID string
	Summary         string
	Description     string
	Location        string
	DTStartUnix     int64
	DTEndUnix       int64
	IsAllDay        bool
	TZName          string
	RRuleText       string
	ICSBlob         string
}

// pendingRow mirrors a row in the pending_writes table for read-back.
type pendingRow struct {
	ID              string
	SourceID        string
	CalendarID      string
	EventID         string
	Op              string
	Scope           string
	Payload         pendingPayload
	Attempt         int
	LastAttemptUnix int64
	LastError       string
	CreatedUnix     int64
}

// pendingPayload is what we JSON-marshal into pending_writes.payload_json.
// It captures everything Drain needs to replay the push (or delete) so
// the events row doesn't have to exist at drain time.
type pendingPayload struct {
	CalendarURL      string      `json:"calendarUrl"`
	UID              string      `json:"uid"`
	ETag             string      `json:"etag,omitempty"`
	Href             string      `json:"href,omitempty"`
	ProviderEventID  string      `json:"providerEventId,omitempty"`
	Summary          string      `json:"summary,omitempty"`
	Description      string      `json:"description,omitempty"`
	Location         string      `json:"location,omitempty"`
	DTStartUnix      int64       `json:"dtstartUnix,omitempty"`
	DTEndUnix        int64       `json:"dtendUnix,omitempty"`
	IsAllDay         bool        `json:"isAllDay,omitempty"`
	TZName           string      `json:"tzName,omitempty"`
	RRuleText        string      `json:"rruleText,omitempty"`
	ICSBlob          string      `json:"icsBlob,omitempty"`
	InstanceTimeUnix int64       `json:"instanceTimeUnix,omitempty"`
	Kind             string      `json:"kind,omitempty"`
	Input            *EventInput `json:"input,omitempty"`
}

// PendingQueue owns the pending_writes table + the drain loop. Shared
// between API (Enqueue on transport failure) and Syncer (Drain after
// sync). Goroutine-safe — methods serialize via the underlying SQLite
// connection.
type PendingQueue struct {
	store   *Store
	secrets coreapi.Secrets
	auth    coreapi.Auth
	events  coreapi.EventBus
}

// NewPendingQueue constructs a queue. events may be nil (skips the
// conflict-event publish on 412); the other deps are required.
func NewPendingQueue(store *Store, secrets coreapi.Secrets, auth coreapi.Auth, events coreapi.EventBus) *PendingQueue {
	return &PendingQueue{
		store:   store,
		secrets: secrets,
		auth:    auth,
		events:  events,
	}
}

// Enqueue persists a new pending row. Returns the row ID.
func (q *PendingQueue) Enqueue(op PendingOp) (string, error) {
	if op.SourceID == "" || op.CalendarID == "" {
		return "", errors.New("pending queue: SourceID and CalendarID required")
	}
	payload, err := json.Marshal(pendingPayload{
		CalendarURL:      op.CalendarURL,
		UID:              op.UID,
		ETag:             op.ETag,
		Href:             op.Href,
		ProviderEventID:  op.ProviderEventID,
		Summary:          op.Summary,
		Description:      op.Description,
		Location:         op.Location,
		DTStartUnix:      op.DTStartUnix,
		DTEndUnix:        op.DTEndUnix,
		IsAllDay:         op.IsAllDay,
		TZName:           op.TZName,
		RRuleText:        op.RRuleText,
		ICSBlob:          op.ICSBlob,
		InstanceTimeUnix: op.InstanceTimeUnix,
		Kind:             string(op.Kind),
		Input:            op.In,
	})
	if err != nil {
		return "", fmt.Errorf("marshal pending payload: %w", err)
	}

	id := uuid.New().String()
	now := time.Now().Unix()
	_, err = q.store.DB().Exec(`
		INSERT INTO pending_writes (
			id, source_id, calendar_id, event_id, op, scope,
			payload_json, attempt, created_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		id, op.SourceID, op.CalendarID, nullIfEmpty(op.EventID),
		string(op.Op), nullIfEmpty(string(op.Scope)),
		string(payload), now,
	)
	if err != nil {
		return "", fmt.Errorf("insert pending_writes: %w", err)
	}
	// Notify the frontend so the composer's success path can surface a
	// "Saved — will sync when online" toast instead of a regular success.
	if q.events != nil {
		_ = q.events.Publish("calendar:write-queued", map[string]any{
			"sourceId": op.SourceID,
			"op":       string(op.Op),
		})
	}
	return id, nil
}

// HasPendingInstance reports whether a scoped split (this /
// this-and-future) for this event + occurrence is already queued.
// updateInstance checks it before pushing so a manual retry while
// offline cannot queue — and later replay — a second copy of the
// same split.
func (q *PendingQueue) HasPendingInstance(eventID string, scope EditScope, instanceUnix int64) (bool, error) {
	rows, err := q.store.DB().Query(`
		SELECT payload_json FROM pending_writes
		WHERE event_id = ? AND scope = ? AND attempt < ?`,
		eventID, string(scope), pendingMaxAttempts)
	if err != nil {
		return false, fmt.Errorf("pending queue: lookup pending split: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return false, fmt.Errorf("pending queue: scan pending split: %w", err)
		}
		var p pendingPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			continue
		}
		if p.InstanceTimeUnix == instanceUnix && p.InstanceTimeUnix != 0 {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Drain replays all pending rows for the given sourceID in
// created_unix ASC order. Stops on ctx cancellation. Per-row outcomes:
//   - success → row deleted, events row updated with new ETag +
//     ProviderEventID where applicable.
//   - ErrTransport → row stays, attempt counter bumped.
//   - ErrConflict (412) → row deleted, calendar:write-conflict event
//     published, user surfaces a "please re-edit" toast via frontend.
//   - other errors → row stays with last_error populated + attempt
//     bumped. Rows past pendingMaxAttempts are skipped on subsequent
//     drains.
//
// Returns the first hard error encountered (ctx canceled, store failure).
// Per-row transport / conflict / hard failures don't bubble up — the
// drain loop continues to the next row so one stuck write doesn't
// block others.
func (q *PendingQueue) Drain(ctx context.Context, sourceID string) error {
	if sourceID == "" {
		return errors.New("pending queue: sourceID required")
	}
	// tried tracks the rows attempted in this pass. A row whose
	// outcome neither deletes it nor bumps its attempt (an auth
	// failure, finding 21) must not be re-picked forever — each row
	// is tried once per Drain, and the next pass retries it.
	tried := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := q.nextPendingSkip(sourceID, tried)
		if err != nil {
			return err
		}
		if row == nil {
			return nil // queue empty for this source
		}
		tried[row.ID] = true
		q.processRow(ctx, *row)
	}
}

// DrainAll iterates all sources known to the store and drains each in
// turn. Used by callers that want a single entry point on the
// system:network-online path.
func (q *PendingQueue) DrainAll(ctx context.Context) error {
	sources, err := q.store.ListSources()
	if err != nil {
		return fmt.Errorf("list sources for drain: %w", err)
	}
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Skip local sources — they can't have pending writes.
		if src.Type == SourceTypeLocal {
			continue
		}
		_ = q.Drain(ctx, src.ID)
	}
	return nil
}

// nextPending returns the oldest pending row for sourceID with attempt <
// pendingMaxAttempts, or nil when none. Skips exhausted rows so they
// don't permanently block the queue.
func (q *PendingQueue) nextPending(sourceID string) (*pendingRow, error) {
	return q.nextPendingSkip(sourceID, nil)
}

// nextPendingSkip is nextPending excluding the given row IDs — Drain uses
// it to try each row once per pass (a row whose outcome neither deletes
// it nor bumps its attempt must not be re-picked forever).
func (q *PendingQueue) nextPendingSkip(sourceID string, skip map[string]bool) (*pendingRow, error) {
	query := `
		SELECT id, source_id, calendar_id, COALESCE(event_id, ''), op,
		       COALESCE(scope, ''), payload_json, attempt,
		       COALESCE(last_attempt_unix, 0), COALESCE(last_error, ''),
		       created_unix
		FROM pending_writes
		WHERE source_id = ? AND attempt < ?
	`
	args := []any{sourceID, pendingMaxAttempts}
	if len(skip) > 0 {
		placeholders := make([]string, 0, len(skip))
		for id := range skip {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query += ` AND id NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY created_unix ASC LIMIT 1`
	rowRes := q.store.DB().QueryRow(query, args...)

	var row pendingRow
	var payloadJSON string
	err := rowRes.Scan(
		&row.ID, &row.SourceID, &row.CalendarID, &row.EventID, &row.Op,
		&row.Scope, &payloadJSON, &row.Attempt,
		&row.LastAttemptUnix, &row.LastError,
		&row.CreatedUnix,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan pending_writes: %w", err)
	}
	if err := json.Unmarshal([]byte(payloadJSON), &row.Payload); err != nil {
		return nil, fmt.Errorf("unmarshal pending payload: %w", err)
	}
	return &row, nil
}

// processRow dispatches one pending row to the provider, handles the
// result (delete row / bump attempt / publish conflict), and returns.
// All per-row errors are absorbed — the row's state in the table reflects
// what happened.
func (q *PendingQueue) processRow(ctx context.Context, row pendingRow) {
	src, err := q.store.GetSource(row.SourceID)
	if err != nil || src == nil {
		// Source deleted (or load failed); drop the orphaned row.
		_ = q.deleteRow(row.ID)
		return
	}

	provider := ProviderForSource(*src, ProviderDeps{
		Store:   q.store,
		Secrets: q.secrets,
		Events:  q.events,
		Auth:    q.auth,
	})

	cal := Calendar{ID: row.CalendarID, URL: row.Payload.CalendarURL}
	ev := Event{
		ID:              row.EventID,
		CalendarID:      row.CalendarID,
		UID:             row.Payload.UID,
		ETag:            row.Payload.ETag,
		Href:            row.Payload.Href,
		ProviderEventID: row.Payload.ProviderEventID,
		Summary:         row.Payload.Summary,
		Description:     row.Payload.Description,
		Location:        row.Payload.Location,
		DTStartUnix:     row.Payload.DTStartUnix,
		DTEndUnix:       row.Payload.DTEndUnix,
		IsAllDay:        row.Payload.IsAllDay,
		TZName:          row.Payload.TZName,
		RRuleText:       row.Payload.RRuleText,
		ICSBlob:         row.Payload.ICSBlob,
	}

	pushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var perr error
	switch PendingOpKind(row.Op) {
	case PendingOpCreate, PendingOpUpdate:
		// A scoped row (this / this-and-future) is a split, not a
		// whole-series write: replay it through PushInstance with
		// the original occurrence time + new fields from the queued
		// payload. The replay also applies the change locally
		// (finding 20): the sync that ran before Drain in this
		// pass already re-fetched the pre-replay server state, so
		// without the local apply the change would stay invisible
		// until the next sync.
		if row.Scope == string(EditScopeThis) || row.Scope == string(EditScopeThisAndFuture) {
			in := EventInput{
				CalendarID:  row.CalendarID,
				Summary:     row.Payload.Summary,
				Description: row.Payload.Description,
				Location:    row.Payload.Location,
				DTStartUnix: row.Payload.DTStartUnix,
				DTEndUnix:   row.Payload.DTEndUnix,
				IsAllDay:    row.Payload.IsAllDay,
				TZName:      row.Payload.TZName,
			}
			if row.Payload.Input != nil {
				in = *row.Payload.Input
			}
			kind := InstanceOpKind(row.Payload.Kind)
			if kind == "" {
				kind = InstanceOpUpdate
			}
			var result PushInstanceResult
			result, perr = provider.PushInstance(pushCtx, *src, cal, PushInstancePayload{
				Master:           ev,
				InstanceTimeUnix: row.Payload.InstanceTimeUnix,
				Op:               EditScope(row.Scope),
				Kind:             kind,
				In:               in,
			})
			if perr == nil {
				q.applyReplayedInstance(row, result, kind, in)
			}
			break
		}
		result, presErr := provider.PushEvent(pushCtx, *src, cal, ev)
		perr = presErr
		if perr == nil {
			// Persist returned ETag + ProviderEventID onto the events row.
			_ = q.updateEventTransportFields(row.CalendarID, ev.UID, result.ETag, result.ProviderEventID)
		}
	case PendingOpDelete:
		// A scoped delete is a split too (EXDATE / instance delete),
		// not a whole-series DELETE.
		if row.Scope == string(EditScopeThis) || row.Scope == string(EditScopeThisAndFuture) {
			var result PushInstanceResult
			result, perr = provider.PushInstance(pushCtx, *src, cal, PushInstancePayload{
				Master:           ev,
				InstanceTimeUnix: row.Payload.InstanceTimeUnix,
				Op:               EditScope(row.Scope),
				Kind:             InstanceOpDelete,
				In:               EventInput{DTStartUnix: row.Payload.InstanceTimeUnix, DTEndUnix: row.Payload.InstanceTimeUnix},
			})
			if perr == nil {
				q.applyReplayedInstance(row, result, InstanceOpDelete, EventInput{})
			}
			break
		}
		perr = provider.DeleteRemote(pushCtx, *src, cal, ev)
		if perr == nil {
			// Finding 28: the sync that ran before Drain in this
			// pass re-created the local row (the server still had
			// the event), and the replay just deleted the server
			// copy — remove the local row in the same step, or the
			// deleted event pops back up until the next sync.
			if err := q.store.WithTx(func(tx *sql.Tx) error {
				return q.store.DeleteEventByUIDTx(tx, row.CalendarID, ev.UID)
			}); err != nil {
				log := logging.WithComponent("calendar")
				log.Warn().Err(err).Str("eventId", row.EventID).Msg("Replayed delete succeeded remotely but the local row could not be removed; the next sync reconciles it")
			}
		}
	default:
		// Unknown op — drop the row.
		_ = q.deleteRow(row.ID)
		return
	}

	q.recordOutcome(row, perr)
}

// applyReplayedInstance applies a scoped replay's result to the local
// rows (audit4 finding 20): the sync that ran before Drain in this pass
// already re-fetched the pre-replay server state, so without this the
// change would stay invisible until the next sync. Best-effort: the
// remote replay already succeeded, so a local-apply failure only logs
// and the next sync reconciles.
func (q *PendingQueue) applyReplayedInstance(row pendingRow, result PushInstanceResult, kind InstanceOpKind, in EventInput) {
	master, err := q.store.GetEvent(row.EventID)
	if err != nil || master == nil {
		return // already gone locally; nothing to apply
	}
	var aerr error
	switch {
	case row.Scope == string(EditScopeThis) && kind == InstanceOpUpdate:
		aerr = applyThisUpdateLocal(q.store, *master, in, row.Payload.InstanceTimeUnix, result)
	case row.Scope == string(EditScopeThis) && kind == InstanceOpDelete:
		aerr = applyThisDeleteLocal(q.store, *master, row.Payload.InstanceTimeUnix, result)
	case row.Scope == string(EditScopeThisAndFuture) && kind == InstanceOpUpdate:
		aerr = applyThisAndFutureUpdateLocal(q.store, *master, in, row.Payload.InstanceTimeUnix, result)
	case row.Scope == string(EditScopeThisAndFuture) && kind == InstanceOpDelete:
		aerr = applyThisAndFutureDeleteLocal(q.store, *master, row.Payload.InstanceTimeUnix, result)
	}
	if aerr != nil {
		log := logging.WithComponent("calendar")
		log.Warn().Err(aerr).Str("eventId", row.EventID).Msg("Replayed scoped change but its local rows could not be updated; the next sync reconciles them")
	}
}

// recordOutcome translates the per-row push result into table state +
// emits a conflict event when applicable.
func (q *PendingQueue) recordOutcome(row pendingRow, perr error) {
	if perr == nil {
		_ = q.deleteRow(row.ID)
		return
	}
	if IsAuthFailure(perr) {
		// Audit4 finding 21: an auth failure is not a failed push —
		// the credentials are stale, not the edit. Do not consume
		// the retry budget: record the failure and leave the row
		// pending for the next Drain after re-authentication.
		// The attempt count is untouched, so the row can never be
		// exhausted by auth failures alone.
		_ = q.noteFailure(row.ID, perr.Error())
		if q.events != nil {
			_ = q.events.Publish("calendar:write-auth-failed", map[string]any{
				"sourceId":   row.SourceID,
				"calendarId": row.CalendarID,
				"eventId":    row.EventID,
				"op":         row.Op,
				"summary":    row.Payload.Summary,
			})
		}
		return
	}
	if errors.Is(perr, ErrConflict) {
		// Drop the row + signal the frontend so the user re-edits. The
		// summary names the event in the toast; the server version was
		// kept, so the next sync re-fetches it.
		_ = q.deleteRow(row.ID)
		if q.events != nil {
			_ = q.events.Publish("calendar:write-conflict", map[string]any{
				"sourceId":   row.SourceID,
				"calendarId": row.CalendarID,
				"eventId":    row.EventID,
				"op":         row.Op,
				"summary":    row.Payload.Summary,
			})
		}
		return
	}
	// Transport or other error: bump attempt + record last_error. Drain
	// will skip rows past pendingMaxAttempts on subsequent passes.
	// When the bump exhausts the row, publish so the calendar UI can
	// surface it (audit4 finding 21) — otherwise the edit would
	// silently never reach the server.
	if n, err := q.bumpAttempt(row.ID, perr.Error()); err == nil && n >= pendingMaxAttempts {
		if q.events != nil {
			_ = q.events.Publish("calendar:write-exhausted", map[string]any{
				"sourceId":   row.SourceID,
				"calendarId": row.CalendarID,
				"eventId":    row.EventID,
				"op":         row.Op,
				"summary":    row.Payload.Summary,
			})
		}
	}
}

func (q *PendingQueue) deleteRow(id string) error {
	_, err := q.store.DB().Exec(`DELETE FROM pending_writes WHERE id = ?`, id)
	return err
}

// bumpAttempt increments a row's attempt counter and records the
// failure, returning the new attempt count.
func (q *PendingQueue) bumpAttempt(id, lastError string) (int, error) {
	if _, err := q.store.DB().Exec(`
		UPDATE pending_writes
		SET attempt = attempt + 1,
		    last_attempt_unix = ?,
		    last_error = ?
		WHERE id = ?`, time.Now().Unix(), lastError, id); err != nil {
		return 0, err
	}
	var attempt int
	if err := q.store.DB().QueryRow(
		`SELECT attempt FROM pending_writes WHERE id = ?`, id,
	).Scan(&attempt); err != nil {
		return 0, err
	}
	return attempt, nil
}

// noteFailure records a failed attempt's error + time WITHOUT consuming
// retry budget. Used for auth failures (audit4 finding 21): the row
// stays pending for the next Drain instead of marching toward
// exhaustion while the credentials are stale.
func (q *PendingQueue) noteFailure(id, lastError string) error {
	_, err := q.store.DB().Exec(`
		UPDATE pending_writes
		SET last_attempt_unix = ?,
		    last_error = ?
		WHERE id = ?`, time.Now().Unix(), lastError, id)
	return err
}

// ExhaustedWrite is one pending row that used up its retry budget
// (audit4 finding 21). Surfaced in the calendar UI with retry/discard.
type ExhaustedWrite struct {
	ID              string `json:"id"`
	SourceID        string `json:"sourceId"`
	CalendarID      string `json:"calendarId"`
	EventID         string `json:"eventId"`
	Op              string `json:"op"`
	Scope           string `json:"scope"`
	Summary         string `json:"summary"`
	LastError       string `json:"lastError"`
	Attempt         int    `json:"attempt"`
	LastAttemptUnix int64  `json:"lastAttemptUnix"`
	CreatedUnix     int64  `json:"createdUnix"`
}

// ListExhausted returns every pending row at or past the retry budget,
// oldest first.
func (q *PendingQueue) ListExhausted() ([]ExhaustedWrite, error) {
	rows, err := q.store.DB().Query(`
		SELECT id, source_id, calendar_id, COALESCE(event_id, ''), op,
		       COALESCE(scope, ''), payload_json, attempt,
		       COALESCE(last_attempt_unix, 0), COALESCE(last_error, ''),
		       created_unix
		FROM pending_writes
		WHERE attempt >= ?
		ORDER BY created_unix ASC`, pendingMaxAttempts)
	if err != nil {
		return nil, fmt.Errorf("list exhausted pending writes: %w", err)
	}
	defer rows.Close()
	var out []ExhaustedWrite
	for rows.Next() {
		var w ExhaustedWrite
		var payloadJSON string
		if err := rows.Scan(
			&w.ID, &w.SourceID, &w.CalendarID, &w.EventID, &w.Op,
			&w.Scope, &payloadJSON, &w.Attempt,
			&w.LastAttemptUnix, &w.LastError, &w.CreatedUnix,
		); err != nil {
			return nil, fmt.Errorf("scan exhausted pending write: %w", err)
		}
		var payload pendingPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err == nil {
			w.Summary = payload.Summary
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ExhaustedCount returns how many pending rows used up their budget.
func (q *PendingQueue) ExhaustedCount() (int, error) {
	var n int
	if err := q.store.DB().QueryRow(
		`SELECT COUNT(*) FROM pending_writes WHERE attempt >= ?`, pendingMaxAttempts,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count exhausted pending writes: %w", err)
	}
	return n, nil
}

// RetryExhausted resets an exhausted row's attempt budget so the next
// Drain retries it. Only exhausted rows can be retried this way — a row
// still inside its budget retries on its own.
func (q *PendingQueue) RetryExhausted(id string) error {
	res, err := q.store.DB().Exec(`
		UPDATE pending_writes
		SET attempt = 0, last_error = '', last_attempt_unix = 0
		WHERE id = ? AND attempt >= ?`, id, pendingMaxAttempts)
	if err != nil {
		return fmt.Errorf("retry exhausted pending write: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("pending write %q is not exhausted", id)
	}
	return nil
}

// DiscardExhausted drops an exhausted row without pushing it. Only
// exhausted rows can be discarded this way — the edit never reaches
// the server, so the user must choose this explicitly in the UI.
func (q *PendingQueue) DiscardExhausted(id string) error {
	res, err := q.store.DB().Exec(
		`DELETE FROM pending_writes WHERE id = ? AND attempt >= ?`, id, pendingMaxAttempts)
	if err != nil {
		return fmt.Errorf("discard exhausted pending write: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("pending write %q is not exhausted", id)
	}
	return nil
}

func (q *PendingQueue) updateEventTransportFields(calendarID, uid, etag, providerEventID string) error {
	_, err := q.store.DB().Exec(`
		UPDATE events
		SET etag = ?, provider_event_id = CASE WHEN ? = '' THEN provider_event_id ELSE ? END
		WHERE calendar_id = ? AND uid = ?`,
		etag, providerEventID, providerEventID, calendarID, uid)
	return err
}
