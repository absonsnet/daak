package mailmerge

import "time"

// Template is the merge template: the live composer's draft fields plus
// the merge-specific attachment field. JSON tags are the wire shape
// shared with the frontend (see extensions/mail-merge/backend/bridge.go).
type Template struct {
	AccountID string `json:"accountId"`
	From      string `json:"from"`
	To        string `json:"to"`
	Cc        string `json:"cc"`
	Bcc       string `json:"bcc"`
	ReplyTo   string `json:"replyTo"`
	Subject   string `json:"subject"`
	BodyHTML  string `json:"bodyHtml"`
	BodyText  string `json:"bodyText"`
	// IsPlainText renders only the text body (no HTML part).
	IsPlainText bool `json:"isPlainText"`
	// Sign requests per-recipient signing of every generated message
	// with the account's configured identity (S/MIME preferred, else
	// PGP — the same precedence as the normal composer). Each row's
	// personalised message is signed individually; the template is
	// never signed once. SignKind is the kind resolved by the host
	// before the run ("smime" | "pgp"); empty when Sign is false.
	// Signing is Phase 3 scope — encryption is never added here.
	Sign     bool   `json:"sign"`
	SignKind string `json:"signKind,omitempty"`
	// GlobalAttachments are the composer's normal attachments: the same
	// file for every recipient, sent as base64 by the frontend.
	GlobalAttachments []GlobalAttachment `json:"globalAttachments"`
	// AttachmentField is the template string for the per-row attachment
	// path, e.g. "{{Attachment}}". One file per line after rendering.
	AttachmentField string `json:"attachmentField"`
}

// GlobalAttachment is one composer attachment as sent by the frontend.
type GlobalAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	DataBase64  string `json:"dataBase64"`
}

// RunRequest is everything a run needs. Rows are the recipient table;
// StartRow/StopRow (1-based inclusive, 0 = first/last) select a slice
// of them — the Thunderbird Start/Stop batch controls and the resume
// mechanism. MessageIDs, when present, is parallel to Rows: the
// Message-ID assigned when that row was first rendered (persisted in
// the run log's RowResult). A resumed run or a held-row batch reuses
// it, so a retry after a lost SMTP acknowledgement rebuilds the same
// mail with the same identity instead of minting a fresh one. Entries
// are "" for rows that were never rendered.
type RunRequest struct {
	Template     Template            `json:"template"`
	Rows         []map[string]string `json:"rows"`
	MessageIDs   []string            `json:"messageIds,omitempty"`
	SourceKind   string              `json:"sourceKind"`  // "csv" | "json" | "xlsx" | "contacts"
	DeliverMode  string              `json:"deliverMode"` // "drafts" | "send" | "later"
	PauseSeconds int                 `json:"pauseSeconds"`
	RandomPause  bool                `json:"randomPause"` // pause is random 0–PauseSeconds
	StartRow     int                 `json:"startRow"`
	StopRow      int                 `json:"stopRow"`
	CapEnabled   bool                `json:"capEnabled"`
	CapLimit     int                 `json:"capLimit"`
	// CapAlreadySent seeds the per-run cap counter with sends this
	// run's lineage already made (audit4 finding 27): a resume or
	// held batch is the same run's continuation, so its budget is
	// the cap minus the lineage's sends, not a fresh cap. The bridge
	// sets it from the stored (inherited) seed plus the run's own
	// SentCount; it persists with the stored request, so a
	// resume-of-a-resume keeps the whole lineage's count.
	CapAlreadySent int    `json:"capAlreadySent,omitempty"`
	AccountID      string `json:"accountId"`
	AttachmentDir  string `json:"attachmentDir"`
	// AlreadySent seeds duplicate suppression when a run is resumed
	// (or a held batch is sent): the lowercased first-To addresses
	// the original run already sent / draft-saved — and, for "later"
	// runs, the addresses still queued. Without the seed, a resumed
	// run re-sends an address the original run already delivered to
	// via a different row (the duplicate check only covers the
	// current run). Persisted with the stored request, so a
	// resume-of-a-resume keeps the whole lineage's addresses: the
	// bridge unions the stored seed with the run's own sent rows
	// when it builds the resume/batch sub-request.
	AlreadySent []string `json:"alreadySent,omitempty"`
}

// RowStatus values for one row's outcome.
const (
	RowSent       = "sent"
	RowDraftSaved = "draft-saved"
	RowFailed     = "failed"
	RowSkipped    = "skipped"
	RowHeld       = "held"
	// RowQueued is a row whose rendered message was placed in the
	// persisted outbox queue (Phase 2 "Send later") instead of being
	// sent or saved as a draft.
	RowQueued = "queued"
)

// RowResult is one row's outcome in a run.
type RowResult struct {
	// Index is the row's position in the request's Rows slice (0-based).
	Index  int    `json:"index"`
	Email  string `json:"email"`  // first To address, "" when unknown
	Status string `json:"status"` // sent | draft-saved | failed | skipped | held
	Error  string `json:"error,omitempty"`
	// MessageID is the Message-ID assigned when the row was rendered
	// ("" when the row never got that far). It is persisted in the
	// run log so a resumed run rebuilds the same mail with the same
	// identity instead of minting a fresh one.
	MessageID string    `json:"messageId,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// RunStatus values for a whole run.
const (
	RunRunning   = "running"
	RunDone      = "done"
	RunCancelled = "cancelled"
	RunHeld      = "held"
	// RunQueued is a "Send later" run whose rows were all placed in the
	// outbox queue: the run itself finished, but nothing was sent yet.
	// The queue runner (or Send Unsent) moves the rows to sent.
	RunQueued = "queued"
	// RunFailed is a run that failed before/without per-row results
	// (e.g. SMTP connect failed, signing pre-flight failed, bad
	// base64 attachment): no row was delivered. It is distinct from
	// RunCancelled — a failed run carries the error and every
	// unprocessed row is recorded RowFailed, so ResumeRun can
	// re-run it.
	RunFailed = "failed"
)

// Run is a persisted merge run with its per-row log.
type Run struct {
	ID           string      `json:"id"`
	CreatedAt    time.Time   `json:"createdAt"`
	AccountID    string      `json:"accountId"`
	DeliverMode  string      `json:"deliverMode"`
	Status       string      `json:"status"` // running | done | cancelled | held | queued | failed
	TotalRows    int         `json:"totalRows"`
	SentCount    int         `json:"sentCount"`
	FailedCount  int         `json:"failedCount"`
	SkippedCount int         `json:"skippedCount"`
	HeldCount    int         `json:"heldCount"`
	QueuedCount  int         `json:"queuedCount"`
	Rows         []RowResult `json:"rows"`
	// Error is the whole-run failure that ended the run (RunFailed
	// only): e.g. the SMTP connect error. "" otherwise.
	Error string `json:"error,omitempty"`
}

// RunSummary is Run without the per-row log, for list views.
type RunSummary struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"createdAt"`
	AccountID    string    `json:"accountId"`
	DeliverMode  string    `json:"deliverMode"`
	Status       string    `json:"status"`
	TotalRows    int       `json:"totalRows"`
	SentCount    int       `json:"sentCount"`
	FailedCount  int       `json:"failedCount"`
	SkippedCount int       `json:"skippedCount"`
	HeldCount    int       `json:"heldCount"`
	QueuedCount  int       `json:"queuedCount"`
	// Error is the whole-run failure (RunFailed only); see Run.Error.
	Error string `json:"error,omitempty"`
}

// Summary derives a RunSummary from a Run.
func (r *Run) Summary() RunSummary {
	return RunSummary{
		ID: r.ID, CreatedAt: r.CreatedAt, AccountID: r.AccountID,
		DeliverMode: r.DeliverMode, Status: r.Status, TotalRows: r.TotalRows,
		SentCount: r.SentCount, FailedCount: r.FailedCount,
		SkippedCount: r.SkippedCount, HeldCount: r.HeldCount,
		QueuedCount: r.QueuedCount, Error: r.Error,
	}
}

// RecomputeCounts recounts the per-status totals from r.Rows.
func (r *Run) RecomputeCounts() {
	r.SentCount, r.FailedCount, r.SkippedCount, r.HeldCount, r.QueuedCount = 0, 0, 0, 0, 0
	for _, row := range r.Rows {
		switch row.Status {
		case RowSent, RowDraftSaved:
			r.SentCount++
		case RowFailed:
			r.FailedCount++
		case RowSkipped:
			r.SkippedCount++
		case RowHeld:
			r.HeldCount++
		case RowQueued:
			r.QueuedCount++
		}
	}
}

// SigningInfo describes an account's signing setup for the merge
// dialog and for pre-flight validation. Kind is "smime" or "pgp"
// when Available; Reason explains why signing is unavailable ("" when
// available) so the dialog can point at the account's Security tab.
type SigningInfo struct {
	Available   bool   `json:"available"`
	Kind        string `json:"kind"`        // "smime" | "pgp" | ""
	DefaultSign bool   `json:"defaultSign"` // the composer's sign policy is "always" for the resolved kind
	Reason      string `json:"reason,omitempty"`
}

// RenderedMessage is one row's rendered template, for preview.
type RenderedMessage struct {
	RowIndex    int      `json:"rowIndex"`
	To          string   `json:"to"`
	Cc          string   `json:"cc"`
	Bcc         string   `json:"bcc"`
	Subject     string   `json:"subject"`
	BodyHTML    string   `json:"bodyHtml"`
	BodyText    string   `json:"bodyText"`
	Attachments []string `json:"attachments"` // resolved per-row attachment paths
}

// ParsedFile is the result of parsing a CSV/JSON/XLSX source file.
type ParsedFile struct {
	Columns     []string            `json:"columns"`
	RowCount    int                 `json:"rowCount"`
	PreviewRows []map[string]string `json:"previewRows"`
	// Sheets is the workbook's sheet names, in order (XLSX only; empty
	// for CSV/JSON). The first sheet is the default.
	Sheets []string `json:"sheets,omitempty"`
}

// ParsedFileFull is the result of parsing a source file with ALL rows
// (not just the 3-row preview). XLSX uses this path: the frontend has
// no XLSX parser, so the backend returns the full table. CSV/JSON keep
// their frontend-local parse (see extensions/mail-merge/frontend/lib/
// mailmerge.ts); this method also serves them for symmetry.
type ParsedFileFull struct {
	Columns  []string            `json:"columns"`
	Rows     []map[string]string `json:"rows"`
	Sheets   []string            `json:"sheets,omitempty"`
	RowCount int                 `json:"rowCount"`
}

// QueuedMessage is one row rendered for the outbox queue: the final
// RFC822 bytes, fixed at queue time — sending just transmits these
// bytes, it never re-renders. From/To are the envelope values the
// SMTP sender needs alongside the bytes.
type QueuedMessage struct {
	RowIndex int
	Email    string // first To address, for the run log + queue view
	Subject  string
	From     string
	To       []string
	Raw      []byte
}

// SliceRows applies the 1-based inclusive StartRow/StopRow range to
// rows (0 = first/last row) and returns the selected rows plus their
// original 0-based indices.
func SliceRows(rows []map[string]string, startRow, stopRow int) ([]map[string]string, []int) {
	start := 0
	if startRow > 1 {
		start = startRow - 1
	}
	stop := len(rows)
	if stopRow > 0 && stopRow < stop {
		stop = stopRow
	}
	if start > len(rows) {
		start = len(rows)
	}
	if stop < start {
		stop = start
	}
	selected := rows[start:stop]
	indices := make([]int, 0, len(selected))
	for i := range selected {
		indices = append(indices, start+i)
	}
	return selected, indices
}
