// Package sendqueue is the persistent Outbox for normal (non-Mail
// Merge) sends: the backing store for Undo Send.
//
// Every send is queued first and dispatched by a background worker
// after the undo window (default 5s, see settings.KeyUndoSendDelay).
// A normal send's queued row holds the full ComposeMessage JSON; the
// worker re-runs the normal send path at dispatch, so signing,
// encryption, Sent saving and draft cleanup behave exactly like a
// direct send. A reaction's row holds pre-built RFC 822 bytes
// instead (ComposeMessage cannot express the reaction's multipart
// structure) and the worker sends them raw.
//
// Sent rows are deleted on success and undone rows on Undo — the
// Outbox keeps no history; the Sent folder is the record. The one
// exception is a StatusSentUnsaved row (re-audit finding R24): a
// mail that was delivered but whose Sent-folder save failed stays
// as a persistent warning, because the Sent folder is not the
// record for that mail — there is no copy of it anywhere else.
// Failed rows stay, with Retry.
//
// This package owns persistence and the retry policy only. The worker
// itself lives in the app package (it needs composeOps), and the
// Mail Merge outbox queue is a separate, untouched system in
// extensions/mail-merge/backend.
package sendqueue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/absonsnet/daak/internal/database"
)

// Item statuses.
const (
	StatusPending   = "pending"   // in the undo window, or waiting on a retry backoff
	StatusSending   = "sending"   // claimed by a dispatcher (transient)
	StatusSent      = "sent"      // kept for Stats/tests; rows are deleted on success, so none persist
	StatusFailed    = "failed"    // permanent error, or attempts exhausted
	StatusCancelled = "cancelled" // kept for Stats/tests; undone rows are deleted, so none persist
	// StatusSentUnsaved marks a mail that WAS delivered but whose
	// Sent-folder save failed (re-audit finding R24): the queue row
	// was already deleted at SMTP accept, so without this row the
	// delivered mail would have no record anywhere in Daak. The row
	// stays in the Outbox as a persistent, user-visible warning
	// naming the subject and recipient — it must NEVER be retried
	// (Retry only handles status 'failed', and the service refuses
	// this status explicitly), because the recipient already has
	// the mail and a retry would deliver a second copy.
	StatusSentUnsaved = "sent_unsaved"
)

// Item kinds. A normal send stores its ComposeMessage JSON and the
// worker re-runs the send path at dispatch; a reaction stores
// pre-built RFC 822 bytes (ComposeMessage cannot express the
// reaction's multipart structure) and the worker sends them raw.
const (
	KindMessage  = "message"
	KindReaction = "reaction"
)

// DefaultMaxAttempts is the send-attempt budget per queued item
// before it is marked failed.
const DefaultMaxAttempts = 5

// Item is one queued normal send. MessageJSON is the full
// smtp.ComposeMessage at queue time; it is never sent to the
// frontend (List omits it — see listColumns). Reaction items
// (KindReaction) carry pre-built bytes in RawMessage instead, plus
// the internal id of the reacted-to message.
type Item struct {
	ID                string     `json:"id"`
	AccountID         string     `json:"accountId"`
	DraftID           string     `json:"draftId"`
	Kind              string     `json:"kind"`
	MessageJSON       string     `json:"-"`
	RawMessage        []byte     `json:"-"`
	ReactionMessageID string     `json:"reactionMessageId,omitempty"`
	Subject           string     `json:"subject"`
	Recipient         string     `json:"recipient"`
	FromAddress       string     `json:"fromAddress"`
	To                []string   `json:"to"`
	Status            string     `json:"status"`
	Attempts          int        `json:"attempts"`
	MaxAttempts       int        `json:"maxAttempts"`
	SendAt            time.Time  `json:"sendAt"`
	DueAt             time.Time  `json:"dueAt"`
	LastError         string     `json:"lastError,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	SentAt            *time.Time `json:"sentAt,omitempty"`
}

// Stats is the whole send queue at a glance, for the Outbox badge.
type Stats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Sent      int `json:"sent"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Store wraps the main DB for the send_queue table.
type Store struct {
	db *database.DB
}

// NewStore creates a Store over the main database. The send_queue
// table is created by migration 50.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

// Enqueue inserts one queued send.
func (s *Store) Enqueue(item Item) error {
	if item.Kind == "" {
		item.Kind = KindMessage
	}
	toRaw, err := json.Marshal(item.To)
	if err != nil {
		return fmt.Errorf("sendqueue: encode recipients: %w", err)
	}
	if item.To == nil {
		toRaw = []byte("[]")
	}
	var sentAt sql.NullString
	if item.SentAt != nil {
		sentAt = sql.NullString{String: item.SentAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	_, err = s.db.Exec(`
		INSERT INTO send_queue
			(id, account_id, draft_id, kind, message_json, raw_message,
			 reaction_message_id, subject, recipient,
			 from_address, to_json, status, attempts, max_attempts,
			 send_at, due_at, last_error, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ID, item.AccountID, item.DraftID, item.Kind, item.MessageJSON,
		item.RawMessage, item.ReactionMessageID, item.Subject,
		item.Recipient, item.FromAddress, string(toRaw), item.Status,
		item.Attempts, item.MaxAttempts,
		item.SendAt.UTC().Format(time.RFC3339Nano),
		item.DueAt.UTC().Format(time.RFC3339Nano),
		item.LastError, item.CreatedAt.UTC().Format(time.RFC3339Nano), sentAt)
	if err != nil {
		return fmt.Errorf("sendqueue: enqueue: %w", err)
	}
	return nil
}

// listColumns is the SELECT list for list views (never the raw
// message JSON or the pre-built reaction bytes — the list view never
// needs either).
const listColumns = `id, account_id, draft_id, kind, reaction_message_id,
	subject, recipient,
	from_address, to_json, status, attempts, max_attempts,
	send_at, due_at, last_error, created_at, sent_at`

func scanItem(rows *sql.Rows) (Item, error) {
	var item Item
	var toRaw string
	var sendAt, dueAt, createdAt string
	var sentAt sql.NullString
	if err := rows.Scan(&item.ID, &item.AccountID, &item.DraftID,
		&item.Kind, &item.ReactionMessageID,
		&item.Subject, &item.Recipient, &item.FromAddress, &toRaw,
		&item.Status, &item.Attempts, &item.MaxAttempts,
		&sendAt, &dueAt, &item.LastError, &createdAt, &sentAt); err != nil {
		return Item{}, err
	}
	if err := json.Unmarshal([]byte(toRaw), &item.To); err != nil {
		return Item{}, err
	}
	if t, err := time.Parse(time.RFC3339Nano, sendAt); err == nil {
		item.SendAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, dueAt); err == nil {
		item.DueAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		item.CreatedAt = t
	}
	if sentAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, sentAt.String); err == nil {
			item.SentAt = &t
		}
	}
	if item.To == nil {
		item.To = []string{}
	}
	return item, nil
}

// Get loads one item without its message JSON. Returns (nil, nil)
// when the item does not exist.
func (s *Store) Get(id string) (*Item, error) {
	rows, err := s.db.Query(`SELECT `+listColumns+` FROM send_queue WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("sendqueue: get: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	item, err := scanItem(rows)
	if err != nil {
		return nil, fmt.Errorf("sendqueue: scan item: %w", err)
	}
	return &item, nil
}

// GetForSend loads one item WITH its message JSON (or, for a
// reaction item, its pre-built bytes), for dispatch.
func (s *Store) GetForSend(id string) (*Item, error) {
	var item Item
	var toRaw string
	var sendAt, dueAt, createdAt string
	var sentAt sql.NullString
	var rawMessage []byte
	err := s.db.QueryRow(`
		SELECT id, account_id, draft_id, kind, message_json, raw_message,
			reaction_message_id, subject, recipient,
			from_address, to_json, status, attempts, max_attempts,
			send_at, due_at, last_error, created_at, sent_at
		FROM send_queue WHERE id = ?
	`, id).Scan(&item.ID, &item.AccountID, &item.DraftID, &item.Kind,
		&item.MessageJSON, &rawMessage, &item.ReactionMessageID,
		&item.Subject, &item.Recipient, &item.FromAddress, &toRaw,
		&item.Status, &item.Attempts, &item.MaxAttempts,
		&sendAt, &dueAt, &item.LastError, &createdAt, &sentAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sendqueue: get for send: %w", err)
	}
	item.RawMessage = rawMessage
	if err := json.Unmarshal([]byte(toRaw), &item.To); err != nil {
		return nil, fmt.Errorf("sendqueue: parse recipients: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, sendAt); err == nil {
		item.SendAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, dueAt); err == nil {
		item.DueAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		item.CreatedAt = t
	}
	if sentAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, sentAt.String); err == nil {
			item.SentAt = &t
		}
	}
	if item.To == nil {
		item.To = []string{}
	}
	return &item, nil
}

// List returns all queue items, newest first. Capped at 500 rows —
// the Outbox is a working list, not an archive.
func (s *Store) List() ([]Item, error) {
	rows, err := s.db.Query(`
		SELECT ` + listColumns + `
		FROM send_queue ORDER BY created_at DESC, id DESC LIMIT 500
	`)
	if err != nil {
		return nil, fmt.Errorf("sendqueue: list: %w", err)
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("sendqueue: scan item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sendqueue: read queue: %w", err)
	}
	return out, nil
}

// ListDue returns pending items whose due time has passed, oldest
// first. At most limit items. Message JSON is NOT loaded (the
// dispatcher loads it per item after claiming).
func (s *Store) ListDue(now time.Time, limit int) ([]Item, error) {
	rows, err := s.db.Query(`
		SELECT `+listColumns+`
		FROM send_queue
		WHERE status = 'pending' AND due_at <= ?
		ORDER BY due_at ASC, id ASC LIMIT ?
	`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("sendqueue: list due: %w", err)
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("sendqueue: scan item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sendqueue: read due: %w", err)
	}
	return out, nil
}

// CountPending counts items still waiting (pending, any due time) —
// the Outbox badge number.
func (s *Store) CountPending() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE status = 'pending'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sendqueue: count pending: %w", err)
	}
	return n, nil
}

// Stats returns global counts by status.
func (s *Store) Stats() (Stats, error) {
	var st Stats
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM send_queue GROUP BY status`)
	if err != nil {
		return st, fmt.Errorf("sendqueue: stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return st, fmt.Errorf("sendqueue: scan stats: %w", err)
		}
		st.Total += n
		switch status {
		case StatusPending:
			st.Pending = n
		case StatusSent:
			st.Sent = n
		case StatusFailed:
			st.Failed = n
		case StatusCancelled:
			st.Cancelled = n
		}
	}
	if err := rows.Err(); err != nil {
		return st, fmt.Errorf("sendqueue: read stats: %w", err)
	}
	return st, nil
}

// Claim atomically moves a pending item to 'sending' (and stamps
// claimed_at). Returns false when the item was not pending — e.g.
// the user undid it, or another dispatcher claimed it. This is the
// cross-process guard: the main process's worker and a manual retry
// share this table and must never send the same item twice.
func (s *Store) Claim(id string, now time.Time) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE send_queue
		SET status = 'sending', claimed_at = ?
		WHERE id = ? AND status = 'pending'
	`, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return false, fmt.Errorf("sendqueue: claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sendqueue: claim rows: %w", err)
	}
	return n == 1, nil
}

// Delete removes a queued item outright. Sent rows are deleted on
// success — the Outbox keeps no history; the Sent folder is the
// record. Returns false when the item does not exist.
func (s *Store) Delete(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM send_queue WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("sendqueue: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sendqueue: delete rows: %w", err)
	}
	return n == 1, nil
}

// MarkSent marks a claimed item sent. Fallback only: when Delete
// fails after a successful send, marking the row sent keeps it from
// being re-queued (and re-sent) by the stale-claim reset. The
// frontend hides sent rows, so a fallback row is invisible.
func (s *Store) MarkSent(id string, sentAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE send_queue
		SET status = 'sent', sent_at = ?, last_error = '', claimed_at = NULL
		WHERE id = ?
	`, sentAt.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("sendqueue: mark sent: %w", err)
	}
	return nil
}

// DeletePending atomically deletes a queued item only while it is
// still pending, for Undo. Returns false when the item is not
// pending — already sending/sent/failed, or already gone. Only a
// pending item can be undone: once the SMTP transaction completes
// the mail is gone and cannot be recalled.
func (s *Store) DeletePending(id string) (bool, error) {
	res, err := s.db.Exec(`
		DELETE FROM send_queue WHERE id = ? AND status = 'pending'
	`, id)
	if err != nil {
		return false, fmt.Errorf("sendqueue: delete pending: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sendqueue: delete pending rows: %w", err)
	}
	return n == 1, nil
}

// RecordAttempt records a failed send attempt: attempts, status
// ('pending' for a retry with a new due time, 'failed' when the
// budget is exhausted or the error is permanent) and the last error.
func (s *Store) RecordAttempt(id string, attempts int, status string, dueAt time.Time, lastErr string) error {
	_, err := s.db.Exec(`
		UPDATE send_queue
		SET attempts = ?, status = ?, due_at = ?, last_error = ?, claimed_at = NULL
		WHERE id = ?
	`, attempts, status, dueAt.UTC().Format(time.RFC3339Nano), lastErr, id)
	if err != nil {
		return fmt.Errorf("sendqueue: record attempt: %w", err)
	}
	return nil
}

// ResetStaleSending returns 'sending' items claimed before `cutoff`
// to 'pending' (a crashed or killed dispatch leaves them claimed;
// the worker resets them on start and on every tick). Their due time
// is left as-is — a stale claim means the send never happened, so
// the item is due immediately. Returns the count.
func (s *Store) ResetStaleSending(cutoff time.Time) (int, error) {
	res, err := s.db.Exec(`
		UPDATE send_queue
		SET status = 'pending', claimed_at = NULL
		WHERE status = 'sending' AND claimed_at IS NOT NULL AND claimed_at < ?
	`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("sendqueue: reset stale sending: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sendqueue: reset stale sending rows: %w", err)
	}
	return int(n), nil
}

// DeletePendingReaction deletes still-pending queued reactions on one
// message (internal id), for RemoveOwnReaction: removing the pill while
// the send is still cancellable cancels the send, so the reaction does
// not go out anyway and reappear on the next Sent sync. Returns the
// number deleted. A reaction already dispatched is untouched —
// removing it stays local-only.
func (s *Store) DeletePendingReaction(messageID string) (int, error) {
	res, err := s.db.Exec(`
		DELETE FROM send_queue
		WHERE kind = 'reaction' AND reaction_message_id = ? AND status = 'pending'
	`, messageID)
	if err != nil {
		return 0, fmt.Errorf("sendqueue: delete pending reaction: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sendqueue: delete pending reaction rows: %w", err)
	}
	return int(n), nil
}

// Retry returns a failed item to the queue, due immediately, with a
// fresh attempt budget. Returns false when the item is not failed.
func (s *Store) Retry(id string, now time.Time) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE send_queue
		SET status = 'pending', attempts = 0, due_at = ?, last_error = '', claimed_at = NULL
		WHERE id = ? AND status = 'failed'
	`, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return false, fmt.Errorf("sendqueue: retry: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sendqueue: retry rows: %w", err)
	}
	return n == 1, nil
}

// HasActiveForDraft reports whether the draft already has a queued
// send that has not finished — status 'pending' (still in the undo
// window or waiting on a retry backoff) or 'sending' (being
// dispatched right now). QueueSend uses it to reject a second queue
// attempt for the same draft: the draft stays visible in Drafts
// during the undo window, and without this guard opening it and
// sending again would enqueue a second row and dispatch twice. The
// pending row itself is the "queued" mark on the draft — no draft
// schema change is needed to carry it.
func (s *Store) HasActiveForDraft(draftID string) (bool, error) {
	if draftID == "" {
		return false, nil
	}
	var n int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM send_queue
		WHERE draft_id = ? AND status IN ('pending', 'sending')
	`, draftID).Scan(&n); err != nil {
		return false, fmt.Errorf("sendqueue: check active draft: %w", err)
	}
	return n > 0, nil
}

// HasFailedForDraft reports whether the draft already has a failed
// queued send sitting in the Outbox. QueueSend uses it to reject a
// re-send from Drafts: the failed row is the one to Retry, and a
// second row would dispatch a second copy (with a new Message-ID)
// alongside a Retry of the old row. A failed row is deliberately
// NOT "active" (see HasActiveForDraft) — the live-row guard and the
// failed-row guard are separate checks with separate errors.
func (s *Store) HasFailedForDraft(draftID string) (bool, error) {
	if draftID == "" {
		return false, nil
	}
	var n int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM send_queue
		WHERE draft_id = ? AND status = 'failed'
	`, draftID).Scan(&n); err != nil {
		return false, fmt.Errorf("sendqueue: check failed draft: %w", err)
	}
	return n > 0, nil
}

// ActiveReaction returns the live queued reaction for one message
// (internal id) — status 'pending' (still in the undo window or
// waiting on a retry backoff) or 'sending' (being dispatched right
// now) — oldest first, or (nil, nil) when there is none. A failed
// reaction row is not live: it stays in the Outbox for Retry and
// does not block a new reaction. QueueReaction uses it to replace a
// pending reaction with the new one, or reject while one is sending.
func (s *Store) ActiveReaction(messageID string) (*Item, error) {
	if messageID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT `+listColumns+`
		FROM send_queue
		WHERE kind = 'reaction' AND reaction_message_id = ?
			AND status IN ('pending', 'sending')
		ORDER BY created_at ASC, id ASC LIMIT 1
	`, messageID)
	if err != nil {
		return nil, fmt.Errorf("sendqueue: active reaction: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	item, err := scanItem(rows)
	if err != nil {
		return nil, fmt.Errorf("sendqueue: scan active reaction: %w", err)
	}
	return &item, nil
}

// RequeueSending is used by tests to put a 'sending' item back to
// pending without waiting for the stale-claim window.
func (s *Store) RequeueSending(id string) error {
	_, err := s.db.Exec(`
		UPDATE send_queue SET status = 'pending', claimed_at = NULL
		WHERE id = ? AND status = 'sending'
	`, id)
	return err
}
