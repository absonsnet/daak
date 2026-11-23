package backend

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/extensions"
	hostmm "github.com/absonsnet/daak/internal/mailmerge"
)

// migrations is the per-extension migration sequence for the Mail Merge
// extension's isolated DB at <dataDir>/extensions/mail-merge/data.db.
// v1: the run log (one row per run + one row per recipient outcome)
// plus the settings blob namespace (ext_kv, created automatically by
// extensions.OpenStore).
// v2 (Phase 2): the persisted outbox queue — one row per queued
// message holding the final rendered RFC822 bytes — plus the
// per-run queue-paused flag and the queued_count tally on runs.
// v3 (v0.7.34): sent and cancelled queue rows are deleted on
// success/cancel instead of lingering as history — the run log
// (mailmerge_run_rows) is the record. One-time cleanup of rows
// left by older versions.
var migrations = []extensions.Migration{
	{
		Version: 1,
		SQL: `
CREATE TABLE IF NOT EXISTS mailmerge_runs (
	id            TEXT PRIMARY KEY,
	account_id    TEXT NOT NULL,
	deliver_mode  TEXT NOT NULL,
	status        TEXT NOT NULL,
	source_kind   TEXT NOT NULL DEFAULT '',
	total_rows    INTEGER NOT NULL DEFAULT 0,
	sent_count    INTEGER NOT NULL DEFAULT 0,
	failed_count  INTEGER NOT NULL DEFAULT 0,
	skipped_count INTEGER NOT NULL DEFAULT 0,
	held_count    INTEGER NOT NULL DEFAULT 0,
	request_json  TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS mailmerge_run_rows (
	run_id    TEXT NOT NULL,
	row_index INTEGER NOT NULL,
	email     TEXT NOT NULL DEFAULT '',
	status    TEXT NOT NULL,
	error     TEXT NOT NULL DEFAULT '',
	timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (run_id, row_index)
);
CREATE INDEX IF NOT EXISTS idx_mailmerge_run_rows_run ON mailmerge_run_rows (run_id);
`,
	},
	{
		Version: 2,
		SQL: `
CREATE TABLE IF NOT EXISTS mailmerge_queue_items (
	id              TEXT PRIMARY KEY,
	run_id          TEXT NOT NULL,
	row_index       INTEGER NOT NULL,
	account_id      TEXT NOT NULL,
	recipient       TEXT NOT NULL DEFAULT '',
	subject         TEXT NOT NULL DEFAULT '',
	from_address    TEXT NOT NULL DEFAULT '',
	to_json         TEXT NOT NULL DEFAULT '[]',
	raw_message     BLOB NOT NULL,
	status          TEXT NOT NULL DEFAULT 'queued',
	attempts        INTEGER NOT NULL DEFAULT 0,
	max_attempts    INTEGER NOT NULL DEFAULT 3,
	next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_error      TEXT NOT NULL DEFAULT '',
	claimed_at      DATETIME,
	created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	sent_at         DATETIME
);
CREATE INDEX IF NOT EXISTS idx_mailmerge_queue_due ON mailmerge_queue_items (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_mailmerge_queue_run ON mailmerge_queue_items (run_id);
ALTER TABLE mailmerge_runs ADD COLUMN queue_paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mailmerge_runs ADD COLUMN queued_count INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		Version: 3,
		SQL: `
DELETE FROM mailmerge_queue_items WHERE status IN ('sent', 'cancelled');
`,
	},
}

// Store wraps the per-extension DB for the Mail Merge extension. Lives
// in an isolated SQLite file, separate from Daak's main DB. No tables
// in this file are read or written by core code; cross-extension access
// flows through coreapi only.
type Store struct {
	*extensions.Store
}

// NewStore opens the Mail Merge extension's isolated SQLite DB and
// applies pending migrations. Called lazily from Bridge.ensureInit on
// the first enabled bridge call (per EXT_RULES.md R10/R11) — a disabled
// extension never opens its DB.
func NewStore(dataDir string) (*Store, error) {
	s, err := extensions.OpenStore(dataDir, "mail-merge", migrations)
	if err != nil {
		return nil, err
	}
	return &Store{Store: s}, nil
}

// settingsKVKey is the ext_kv key under which one account's settings
// JSON blob is stored.
func settingsKVKey(accountID string) string {
	return "settings:" + accountID
}

// GetSettings reads one account's stored settings blob. Returns the
// defaults when nothing has been saved yet.
func (s *Store) GetSettings(accountID string) (Settings, error) {
	st := DefaultSettings()
	if accountID == "" {
		return st, nil
	}
	raw, err := s.KV().Get(settingsKVKey(accountID))
	if err != nil {
		return Settings{}, fmt.Errorf("mail-merge: read settings: %w", err)
	}
	if raw == "" {
		return st, nil
	}
	// Decode over the defaults so settings saved by an older version
	// (missing newer fields) still yield sane values for those fields.
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return Settings{}, fmt.Errorf("mail-merge: parse stored settings: %w", err)
	}
	return st, nil
}

// SaveSettings persists one account's settings blob.
func (s *Store) SaveSettings(accountID string, st Settings) error {
	if accountID == "" {
		return fmt.Errorf("mail-merge: account is required")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("mail-merge: encode settings: %w", err)
	}
	if err := s.KV().Set(settingsKVKey(accountID), string(raw)); err != nil {
		return fmt.Errorf("mail-merge: save settings: %w", err)
	}
	return nil
}

// SaveRun inserts a new run record with its full request JSON (template
// + rows + options, so resume/batch operations can rebuild the run
// without the frontend re-sending the table).
func (s *Store) SaveRun(run *hostmm.Run, req hostmm.RunRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("mail-merge: encode run request: %w", err)
	}
	_, err = s.DB().Exec(`
		INSERT INTO mailmerge_runs
			(id, account_id, deliver_mode, status, source_kind, total_rows,
			 sent_count, failed_count, skipped_count, held_count, queued_count, request_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, run.ID, run.AccountID, run.DeliverMode, run.Status, req.SourceKind,
		run.TotalRows, run.SentCount, run.FailedCount, run.SkippedCount, run.HeldCount,
		run.QueuedCount,
		string(raw), run.CreatedAt.UTC().Format(time.RFC3339Nano), run.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("mail-merge: save run: %w", err)
	}
	return nil
}

// UpdateRunStatusAndCounts rewrites a run's status + counts from the
// in-memory Run (after progress or completion).
func (s *Store) UpdateRunStatusAndCounts(run *hostmm.Run) error {
	_, err := s.DB().Exec(`
		UPDATE mailmerge_runs
		SET status = ?, sent_count = ?, failed_count = ?, skipped_count = ?,
			held_count = ?, queued_count = ?, total_rows = ?, updated_at = ?
		WHERE id = ?
	`, run.Status, run.SentCount, run.FailedCount, run.SkippedCount, run.HeldCount,
		run.QueuedCount,
		run.TotalRows, time.Now().UTC().Format(time.RFC3339Nano), run.ID)
	if err != nil {
		return fmt.Errorf("mail-merge: update run: %w", err)
	}
	return nil
}

// SaveRowResult upserts one row's outcome (held rows are later updated
// in place by batch operations, hence INSERT ... ON CONFLICT).
func (s *Store) SaveRowResult(runID string, r hostmm.RowResult) error {
	_, err := s.DB().Exec(`
		INSERT INTO mailmerge_run_rows (run_id, row_index, email, status, error, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, row_index) DO UPDATE SET
			email = excluded.email, status = excluded.status,
			error = excluded.error, timestamp = excluded.timestamp
	`, runID, r.Index, r.Email, r.Status, r.Error, r.Timestamp.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("mail-merge: save run row: %w", err)
	}
	return nil
}

// GetRun loads a run with its per-row log. Returns (nil, nil) when the
// run does not exist.
func (s *Store) GetRun(runID string) (*hostmm.Run, error) {
	var run hostmm.Run
	var createdAt, updatedAt string
	err := s.DB().QueryRow(`
		SELECT id, account_id, deliver_mode, status, total_rows,
			sent_count, failed_count, skipped_count, held_count, queued_count, created_at, updated_at
		FROM mailmerge_runs WHERE id = ?
	`, runID).Scan(&run.ID, &run.AccountID, &run.DeliverMode, &run.Status,
		&run.TotalRows, &run.SentCount, &run.FailedCount, &run.SkippedCount,
		&run.HeldCount, &run.QueuedCount, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mail-merge: get run: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		run.CreatedAt = t
	}
	rows, err := s.DB().Query(`
		SELECT row_index, email, status, error, timestamp
		FROM mailmerge_run_rows WHERE run_id = ? ORDER BY row_index ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("mail-merge: get run rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r hostmm.RowResult
		var ts string
		if err := rows.Scan(&r.Index, &r.Email, &r.Status, &r.Error, &ts); err != nil {
			return nil, fmt.Errorf("mail-merge: scan run row: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			r.Timestamp = t
		}
		run.Rows = append(run.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mail-merge: read run rows: %w", err)
	}
	if run.Rows == nil {
		run.Rows = []hostmm.RowResult{}
	}
	return &run, nil
}

// GetRunRequest loads the stored request JSON for a run (needed by
// resume/batch operations). Returns an error when the run or its
// request is missing.
func (s *Store) GetRunRequest(runID string) (hostmm.RunRequest, error) {
	var raw string
	err := s.DB().QueryRow(`SELECT request_json FROM mailmerge_runs WHERE id = ?`, runID).Scan(&raw)
	if err == sql.ErrNoRows {
		return hostmm.RunRequest{}, errors.New("mail-merge: run not found")
	}
	if err != nil {
		return hostmm.RunRequest{}, fmt.Errorf("mail-merge: get run request: %w", err)
	}
	var req hostmm.RunRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return hostmm.RunRequest{}, fmt.Errorf("mail-merge: parse run request: %w", err)
	}
	return req, nil
}

// ListRuns returns run summaries, newest first.
func (s *Store) ListRuns() ([]hostmm.RunSummary, error) {
	rows, err := s.DB().Query(`
		SELECT id, account_id, deliver_mode, status, total_rows,
			sent_count, failed_count, skipped_count, held_count, queued_count, created_at
		FROM mailmerge_runs ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("mail-merge: list runs: %w", err)
	}
	defer rows.Close()
	out := []hostmm.RunSummary{}
	for rows.Next() {
		var sum hostmm.RunSummary
		var createdAt string
		if err := rows.Scan(&sum.ID, &sum.AccountID, &sum.DeliverMode, &sum.Status,
			&sum.TotalRows, &sum.SentCount, &sum.FailedCount, &sum.SkippedCount,
			&sum.HeldCount, &sum.QueuedCount, &createdAt); err != nil {
			return nil, fmt.Errorf("mail-merge: scan run: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			sum.CreatedAt = t
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mail-merge: read runs: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Outbox queue (Phase 2)
// ---------------------------------------------------------------------------
// One row per queued message. The rendered RFC822 bytes are stored at
// queue time, so the message is fixed when queued and sending just
// transmits those bytes. Times are stored as RFC3339Nano UTC strings
// (the same convention as the run log).

// queueConfigKVKey is the ext_kv key for the global queue config blob.
func queueConfigKVKey() string { return "queue:config" }

// GetQueueConfig reads the global queue config (per-hour budget +
// global pause). Defaults: 100/hour, not paused.
func (s *Store) GetQueueConfig() (QueueConfig, error) {
	cfg := DefaultQueueConfig()
	raw, err := s.KV().Get(queueConfigKVKey())
	if err != nil {
		return QueueConfig{}, fmt.Errorf("mail-merge: read queue config: %w", err)
	}
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return QueueConfig{}, fmt.Errorf("mail-merge: parse queue config: %w", err)
	}
	if cfg.PerHour < 1 {
		cfg.PerHour = DefaultQueueConfig().PerHour
	}
	return cfg, nil
}

// SaveQueueConfig persists the global queue config.
func (s *Store) SaveQueueConfig(cfg QueueConfig) error {
	if cfg.PerHour < 1 {
		return fmt.Errorf("mail-merge: queue per-hour limit must be at least 1")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("mail-merge: encode queue config: %w", err)
	}
	if err := s.KV().Set(queueConfigKVKey(), string(raw)); err != nil {
		return fmt.Errorf("mail-merge: save queue config: %w", err)
	}
	return nil
}

// EnqueueItem inserts one rendered message into the outbox queue.
func (s *Store) EnqueueItem(item QueuedItem) error {
	toRaw, err := json.Marshal(item.To)
	if err != nil {
		return fmt.Errorf("mail-merge: encode queue recipients: %w", err)
	}
	_, err = s.DB().Exec(`
		INSERT INTO mailmerge_queue_items
			(id, run_id, row_index, account_id, recipient, subject,
			 from_address, to_json, raw_message, status, attempts,
			 max_attempts, next_attempt_at, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ID, item.RunID, item.RowIndex, item.AccountID, item.Recipient,
		item.Subject, item.FromAddress, string(toRaw), item.RawMessage,
		item.Status, item.Attempts, item.MaxAttempts,
		item.NextAttemptAt.UTC().Format(time.RFC3339Nano), item.LastError,
		item.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("mail-merge: enqueue item: %w", err)
	}
	return nil
}

// queueItemColumns is the SELECT list for queue items WITHOUT the raw
// message bytes (the list view never needs them).
const queueItemColumns = `id, run_id, row_index, account_id, recipient, subject,
	from_address, to_json, status, attempts, max_attempts,
	next_attempt_at, last_error, created_at, sent_at`

// scanQueueItem scans one queue row (without raw_message) into a
// QueuedItem.
func scanQueueItem(rows *sql.Rows) (QueuedItem, error) {
	var item QueuedItem
	var toRaw string
	var nextAttemptAt, createdAt string
	var sentAt sql.NullString
	if err := rows.Scan(&item.ID, &item.RunID, &item.RowIndex, &item.AccountID,
		&item.Recipient, &item.Subject, &item.FromAddress, &toRaw, &item.Status,
		&item.Attempts, &item.MaxAttempts, &nextAttemptAt, &item.LastError,
		&createdAt, &sentAt); err != nil {
		return QueuedItem{}, err
	}
	if err := json.Unmarshal([]byte(toRaw), &item.To); err != nil {
		return QueuedItem{}, err
	}
	if t, err := time.Parse(time.RFC3339Nano, nextAttemptAt); err == nil {
		item.NextAttemptAt = t
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

// ListQueueItems returns all queue items, oldest first (raw bytes not
// loaded). Capped at 1000 rows — the queue is a working list, not an
// archive; sent items stay visible until the user clears them (a
// future cleanup; see queue.go).
func (s *Store) ListQueueItems() ([]QueuedItem, error) {
	rows, err := s.DB().Query(`
		SELECT ` + queueItemColumns + `
		FROM mailmerge_queue_items ORDER BY created_at ASC, run_id ASC, row_index ASC, id ASC LIMIT 1000
	`)
	if err != nil {
		return nil, fmt.Errorf("mail-merge: list queue: %w", err)
	}
	defer rows.Close()
	out := []QueuedItem{}
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			return nil, fmt.Errorf("mail-merge: scan queue item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mail-merge: read queue: %w", err)
	}
	return out, nil
}

// GetQueueItem loads one queue item without its raw bytes. Returns
// (nil, nil) when the item does not exist.
func (s *Store) GetQueueItem(id string) (*QueuedItem, error) {
	rows, err := s.DB().Query(`
		SELECT `+queueItemColumns+`
		FROM mailmerge_queue_items WHERE id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("mail-merge: get queue item: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	item, err := scanQueueItem(rows)
	if err != nil {
		return nil, fmt.Errorf("mail-merge: scan queue item: %w", err)
	}
	return &item, nil
}

// GetQueueItemForSend loads one queue item WITH its raw message bytes,
// for the dispatch path.
func (s *Store) GetQueueItemForSend(id string) (*QueuedItem, error) {
	var item QueuedItem
	var toRaw string
	var nextAttemptAt, createdAt string
	var sentAt sql.NullString
	err := s.DB().QueryRow(`
		SELECT id, run_id, row_index, account_id, recipient, subject,
			from_address, to_json, raw_message, status, attempts, max_attempts,
			next_attempt_at, last_error, created_at, sent_at
		FROM mailmerge_queue_items WHERE id = ?
	`, id).Scan(&item.ID, &item.RunID, &item.RowIndex, &item.AccountID,
		&item.Recipient, &item.Subject, &item.FromAddress, &toRaw,
		&item.RawMessage, &item.Status, &item.Attempts, &item.MaxAttempts,
		&nextAttemptAt, &item.LastError, &createdAt, &sentAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mail-merge: get queue item: %w", err)
	}
	if err := json.Unmarshal([]byte(toRaw), &item.To); err != nil {
		return nil, fmt.Errorf("mail-merge: parse queue recipients: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, nextAttemptAt); err == nil {
		item.NextAttemptAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		item.CreatedAt = t
	}
	if sentAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, sentAt.String); err == nil {
			item.SentAt = &t
		}
	}
	return &item, nil
}

// ListDueItems returns queued items whose next attempt is due, oldest
// first. At most limit items. Raw bytes are NOT loaded (the dispatcher
// loads them per item). When includePaused is false (the background
// runner), items of queue-paused runs are excluded; a manual Send
// Unsent passes true — the user explicitly asked for these messages.
func (s *Store) ListDueItems(now time.Time, limit int, runID string, includePaused bool) ([]QueuedItem, error) {
	q := `
		SELECT ` + queueColumnsQualified() + `
		FROM mailmerge_queue_items q
		JOIN mailmerge_runs r ON r.id = q.run_id
		WHERE q.status = 'queued' AND q.next_attempt_at <= ?
	`
	args := []any{now.UTC().Format(time.RFC3339Nano)}
	if !includePaused {
		q += ` AND COALESCE(r.queue_paused, 0) = 0`
	}
	if runID != "" {
		q += ` AND q.run_id = ?`
		args = append(args, runID)
	}
	q += ` ORDER BY q.created_at ASC, q.id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB().Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("mail-merge: list due queue items: %w", err)
	}
	defer rows.Close()
	out := []QueuedItem{}
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			return nil, fmt.Errorf("mail-merge: scan queue item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mail-merge: read due queue items: %w", err)
	}
	return out, nil
}

// queueColumnsQualified returns queueItemColumns with the q. alias, for
// the JOIN query in ListDueItems.
func queueColumnsQualified() string {
	cols := []string{"id", "run_id", "row_index", "account_id", "recipient",
		"subject", "from_address", "to_json", "status", "attempts",
		"max_attempts", "next_attempt_at", "last_error", "created_at", "sent_at"}
	for i, c := range cols {
		cols[i] = "q." + c
	}
	return strings.Join(cols, ", ")
}

// CountSentSince counts run-log rows marked sent at or after `since` —
// the hourly budget check. It reads the run log, not the queue:
// sent queue rows are deleted on success, and the run log is the
// record. (Direct sends also write RowSent rows, so they count toward
// the budget too — more conservative than before, when only queued
// sends counted.)
func (s *Store) CountSentSince(since time.Time) (int, error) {
	var n int
	err := s.DB().QueryRow(`
		SELECT COUNT(*) FROM mailmerge_run_rows
		WHERE status = 'sent' AND timestamp >= ?
	`, since.UTC().Format(time.RFC3339Nano)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mail-merge: count sent since: %w", err)
	}
	return n, nil
}

// OldestSentSince returns the earliest sent timestamp at or after
// `since` (the moment the hourly budget frees up again), from the
// run log. False when nothing was sent in the window.
func (s *Store) OldestSentSince(since time.Time) (time.Time, bool, error) {
	var raw sql.NullString
	err := s.DB().QueryRow(`
		SELECT MIN(timestamp) FROM mailmerge_run_rows
		WHERE status = 'sent' AND timestamp >= ?
	`, since.UTC().Format(time.RFC3339Nano)).Scan(&raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mail-merge: oldest sent since: %w", err)
	}
	if !raw.Valid {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mail-merge: parse oldest sent: %w", err)
	}
	return t, true, nil
}

// CountQueued counts items still queued (any next-attempt time),
// optionally for one run.
func (s *Store) CountQueued(runID string) (int, error) {
	q := `SELECT COUNT(*) FROM mailmerge_queue_items WHERE status = 'queued'`
	var args []any
	if runID != "" {
		q += ` AND run_id = ?`
		args = append(args, runID)
	}
	var n int
	if err := s.DB().QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("mail-merge: count queued: %w", err)
	}
	return n, nil
}

// CountQueuedDue counts items still queued whose next attempt is due
// at or before `now` (optionally for one run).
func (s *Store) CountQueuedDue(now time.Time, runID string) (int, error) {
	q := `SELECT COUNT(*) FROM mailmerge_queue_items WHERE status = 'queued' AND next_attempt_at <= ?`
	args := []any{now.UTC().Format(time.RFC3339Nano)}
	if runID != "" {
		q += ` AND run_id = ?`
		args = append(args, runID)
	}
	var n int
	if err := s.DB().QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("mail-merge: count queued due: %w", err)
	}
	return n, nil
}

// ClaimItem atomically moves a queued item to 'sending' (and stamps
// claimed_at). Returns false when the item was not queued — e.g. the
// other process's runner already claimed it. This is the cross-process
// guard: the main process's runner and a detached composer's manual
// Send Unsent share this SQLite file and must never send the same
// item twice.
func (s *Store) ClaimItem(id string, now time.Time) (bool, error) {
	res, err := s.DB().Exec(`
		UPDATE mailmerge_queue_items
		SET status = 'sending', claimed_at = ?
		WHERE id = ? AND status = 'queued'
	`, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return false, fmt.Errorf("mail-merge: claim queue item: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mail-merge: claim queue item rows: %w", err)
	}
	return n == 1, nil
}

// MarkItemSent marks a claimed item sent. It is only the fallback
// when deleting the row fails (a sent mail must never be re-queued):
// the normal path deletes the row — see DeleteQueueItem.
func (s *Store) MarkItemSent(id string, sentAt time.Time) error {
	_, err := s.DB().Exec(`
		UPDATE mailmerge_queue_items
		SET status = 'sent', sent_at = ?, last_error = '', claimed_at = NULL
		WHERE id = ?
	`, sentAt.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("mail-merge: mark queue item sent: %w", err)
	}
	return nil
}

// DeleteQueueItem deletes one queued/sending item — the normal
// outcome of a successful send or a cancel. Returns false when the
// item is already sent/failed (or gone).
func (s *Store) DeleteQueueItem(id string) (bool, error) {
	res, err := s.DB().Exec(`
		DELETE FROM mailmerge_queue_items
		WHERE id = ? AND status IN ('queued', 'sending')
	`, id)
	if err != nil {
		return false, fmt.Errorf("mail-merge: delete queue item: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mail-merge: delete queue item rows: %w", err)
	}
	return n == 1, nil
}

// DeleteRunQueueItems deletes every queued/sending item of a run.
// Returns the deleted count.
func (s *Store) DeleteRunQueueItems(runID string) (int, error) {
	res, err := s.DB().Exec(`
		DELETE FROM mailmerge_queue_items
		WHERE run_id = ? AND status IN ('queued', 'sending')
	`, runID)
	if err != nil {
		return 0, fmt.Errorf("mail-merge: delete run queue items: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mail-merge: delete run queue items rows: %w", err)
	}
	return int(n), nil
}

// UpdateItemAttempt records a failed send attempt: attempts, status
// ('queued' for a retry, 'failed' when attempts hit max_attempts),
// next attempt time and the last error.
func (s *Store) UpdateItemAttempt(id string, attempts int, status string, nextAttemptAt time.Time, lastErr string) error {
	_, err := s.DB().Exec(`
		UPDATE mailmerge_queue_items
		SET attempts = ?, status = ?, next_attempt_at = ?, last_error = ?, claimed_at = NULL
		WHERE id = ?
	`, attempts, status, nextAttemptAt.UTC().Format(time.RFC3339Nano), lastErr, id)
	if err != nil {
		return fmt.Errorf("mail-merge: update queue item attempt: %w", err)
	}
	return nil
}

// ResetStaleSending returns 'sending' items claimed before `cutoff` to
// 'queued' (a crashed or killed dispatch leaves them claimed; the
// runner resets them on start and on every tick). Returns the count.
func (s *Store) ResetStaleSending(cutoff time.Time) (int, error) {
	res, err := s.DB().Exec(`
		UPDATE mailmerge_queue_items
		SET status = 'queued', claimed_at = NULL
		WHERE status = 'sending' AND claimed_at IS NOT NULL AND claimed_at < ?
	`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("mail-merge: reset stale sending: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mail-merge: reset stale sending rows: %w", err)
	}
	return int(n), nil
}

// PushDueItemsNextAttempt moves the next-attempt time of all still-due
// queued items to `at` (optionally for one run), excluding queue-paused
// runs (their items wait for an explicit resume, not the budget clock).
// The runner uses this when the hourly budget is exhausted so the
// persisted schedule shows when the next batch can go out — and
// survives an app restart.
func (s *Store) PushDueItemsNextAttempt(now time.Time, at time.Time, runID string) error {
	q := `UPDATE mailmerge_queue_items SET next_attempt_at = ?
		WHERE status = 'queued' AND next_attempt_at <= ?
		AND run_id IN (SELECT id FROM mailmerge_runs WHERE COALESCE(queue_paused, 0) = 0)`
	args := []any{at.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano)}
	if runID != "" {
		q += ` AND run_id = ?`
		args = append(args, runID)
	}
	if _, err := s.DB().Exec(q, args...); err != nil {
		return fmt.Errorf("mail-merge: push due items: %w", err)
	}
	return nil
}

// RetryFailedRunItems returns a run's failed items to the queue
// (attempts reset, due now). Returns the retried count.
func (s *Store) RetryFailedRunItems(runID string, now time.Time) (int, error) {
	res, err := s.DB().Exec(`
		UPDATE mailmerge_queue_items
		SET status = 'queued', attempts = 0, next_attempt_at = ?, last_error = '', claimed_at = NULL
		WHERE run_id = ? AND status = 'failed'
	`, now.UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return 0, fmt.Errorf("mail-merge: retry failed run items: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mail-merge: retry failed run items rows: %w", err)
	}
	return int(n), nil
}

// SetRunQueuePaused sets a run's queue-paused flag.
func (s *Store) SetRunQueuePaused(runID string, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	if _, err := s.DB().Exec(`UPDATE mailmerge_runs SET queue_paused = ? WHERE id = ?`, v, runID); err != nil {
		return fmt.Errorf("mail-merge: set run queue paused: %w", err)
	}
	return nil
}

// QueueStats returns global counts by status plus per-run breakdowns
// (runs that still have queue items, with their paused flag).
func (s *Store) QueueStats() (QueueStats, error) {
	var st QueueStats
	rows, err := s.DB().Query(`
		SELECT status, COUNT(*) FROM mailmerge_queue_items GROUP BY status
	`)
	if err != nil {
		return st, fmt.Errorf("mail-merge: queue stats: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return st, fmt.Errorf("mail-merge: scan queue stats: %w", err)
		}
		st.Total += n
		switch status {
		case "queued":
			st.Queued = n
		case "sent":
			st.Sent = n
		case "failed":
			st.Failed = n
		case "cancelled":
			st.Cancelled = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, fmt.Errorf("mail-merge: read queue stats: %w", err)
	}
	runRows, err := s.DB().Query(`
		SELECT q.run_id, q.status, COUNT(*), COALESCE(r.queue_paused, 0)
		FROM mailmerge_queue_items q
		LEFT JOIN mailmerge_runs r ON r.id = q.run_id
		GROUP BY q.run_id, q.status, r.queue_paused
		ORDER BY MIN(q.created_at) DESC
	`)
	if err != nil {
		return st, fmt.Errorf("mail-merge: queue run stats: %w", err)
	}
	defer runRows.Close()
	byRun := map[string]*QueueRunStats{}
	var order []string
	for runRows.Next() {
		var runID, status string
		var n, paused int
		if err := runRows.Scan(&runID, &status, &n, &paused); err != nil {
			return st, fmt.Errorf("mail-merge: scan queue run stats: %w", err)
		}
		rs, ok := byRun[runID]
		if !ok {
			rs = &QueueRunStats{RunID: runID, Paused: paused == 1}
			byRun[runID] = rs
			order = append(order, runID)
		}
		switch status {
		case "queued":
			rs.Queued = n
		case "sent":
			rs.Sent = n
		case "failed":
			rs.Failed = n
		case "cancelled":
			rs.Cancelled = n
		}
	}
	if err := runRows.Err(); err != nil {
		return st, fmt.Errorf("mail-merge: read queue run stats: %w", err)
	}
	st.Runs = []QueueRunStats{}
	for _, id := range order {
		st.Runs = append(st.Runs, *byRun[id])
	}
	return st, nil
}
