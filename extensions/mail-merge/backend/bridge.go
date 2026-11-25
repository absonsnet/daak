package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
	"github.com/absonsnet/daak/internal/logging"
	hostmm "github.com/absonsnet/daak/internal/mailmerge"
	"github.com/absonsnet/daak/internal/platform"
	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// MailMergeBridge is the Wails-bindable surface for the Mail Merge
// extension. It's embedded into the host `*app.App` and `*app.ComposerApp`
// structs; Go's method-promotion makes every MailMergeBridge method
// appear on App/ComposerApp so Wails' reflection-based bind generator
// picks them up.
//
// Method naming: all Wails-bound bridge methods use the `MailMerge_`
// prefix so they can't collide with another extension's methods after
// embedding into the same App. See docs/EXT_RULES.md R19.
//
// Lightweight-by-default invariant: when the user has the Mail Merge
// extension disabled, NOTHING is loaded beyond the ~80-byte bridge struct
// itself. The per-extension SQLite is lazy-init via sync.Once inside
// `ensureInit`. The first enabled method call triggers init; subsequent
// calls are fast. See docs/EXT_RULES.md §4.
//
// The bridge owns persistence (run log + per-account settings in its
// SQLite) and the run lifecycle (async execution, cancel, resume,
// cap batches). The actual merge work — rendering, sending, draft
// saving — lives in the host-side internal/mailmerge Service, injected
// as a plain object in MailMergeBridgeDeps. Per docs/EXT_RULES.md R2 the
// deps hold NO closures wrapping internal/* calls.
type MailMergeBridge struct {
	deps MailMergeBridgeDeps

	// Lazy-initialized state. Constructed on the first enabled bridge
	// call so disabled extensions contribute zero work.
	initOnce sync.Once
	initErr  error
	store    *Store

	// Runs in flight, plus the session's picked attachment folder.
	// The attachment folder is session state only (Thunderbird's
	// MailExtension limitation applies here too: picked files are not
	// remembered across runs) — set by MailMerge_PickAttachmentDir and
	// used by Validate/Preview/StartRun when the request carries none.
	mu            sync.Mutex
	active        map[string]*activeRun
	attachmentDir string

	// Queue runner state. The runner is started explicitly by the
	// MAIN process only (App.Startup → StartQueueRunner); see queue.go.
	// nowFunc / queueTickIntervalOverride / queueMaxAttemptsOverride
	// are test hooks (nil/0 = production defaults).
	runnerRunning             bool
	runnerStop                chan struct{}
	nowFunc                   func() time.Time
	queueTickIntervalOverride time.Duration
	queueMaxAttemptsOverride  int
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// MailMergeBridgeDeps bundles the host-provided dependencies the bridge
// needs. Grouped into a struct so adding a new dep doesn't churn every
// call site in the host. Per docs/EXT_RULES.md R2, this struct holds NO
// closures wrapping `internal/*` calls — the Service object is the one
// host-provided collaborator, constructed by the host from its stores.
type MailMergeBridgeDeps struct {
	// SettingsStore is consulted on every bridge call for the enabled
	// gate (lightweight invariant — disabled calls short-circuit before
	// any work).
	SettingsStore SettingsStore

	// Paths gives the bridge access to the OS-appropriate data directory
	// for opening the extension's per-extension SQLite.
	Paths *platform.Paths

	// Core is the coreapi handle. Not used by Mail Merge v1 (no secrets,
	// no OAuth) — kept for symmetry with the other first-party
	// extensions and forward-compat. May be nil.
	Core coreapi.Core

	// Service is the host-constructed merge service (draft saver, SMTP
	// factory, Sent saver, contact lister). Required: without it no run
	// can execute and GetContacts has no source.
	Service *hostmm.Service

	// Ctx is the Wails context, needed for file dialogs and run-update
	// events. May be nil (dialogs then error instead of panicking).
	Ctx context.Context
}

// SettingsStore is the narrow interface the bridge needs from the host's
// settings store. Defined here (rather than importing the concrete type)
// so 3rd-party extensions can swap in their own implementation for tests
// and so this file doesn't grow a host-package dependency.
type SettingsStore interface {
	IsExtensionEnabled(id string) (bool, error)
}

// NewMailMergeBridge constructs the bridge with its dependencies. Does
// NOT touch the DB or open any extension state — that's ensureInit's job,
// on the first enabled bridge call.
func NewMailMergeBridge(deps MailMergeBridgeDeps) *MailMergeBridge {
	return &MailMergeBridge{deps: deps, active: map[string]*activeRun{}}
}

// extensionID is the key the bridge looks up in settings for the
// enabled-state check. Kept as a const so a typo doesn't silently
// disable every bridge method.
const extensionID = "mail-merge"

// ContactsResult is the single-struct return of MailMerge_GetContacts.
// NOTE (contract deviation): the Phase 1 brief specified
// GetContacts() (rows, columns, error), but Wails v2 only supports one
// or two return values per bound method (a third value is silently
// dropped by the runtime's Call dispatcher), so rows + columns are
// returned in one struct. The frontend reads result.rows / result.columns.
type ContactsResult struct {
	Rows    []map[string]string `json:"rows"`
	Columns []string            `json:"columns"`
}

// gateEnabled returns true when the extension is currently enabled AND
// the host gave us a SettingsStore. Returns false (silently) when the
// store is nil or when the settings read errors out.
func (b *MailMergeBridge) gateEnabled() bool {
	if b.deps.SettingsStore == nil {
		return false
	}
	enabled, err := b.deps.SettingsStore.IsExtensionEnabled(extensionID)
	if err != nil {
		return false
	}
	return enabled
}

// ensureInit lazily opens the per-extension SQLite (via
// extensions.OpenStore, migrations in store.go). Runs once per process;
// subsequent calls return the first call's result.
func (b *MailMergeBridge) ensureInit() error {
	b.initOnce.Do(func() {
		if b.deps.Paths == nil {
			b.initErr = errors.New("mail-merge.MailMergeBridge: missing Paths in deps")
			return
		}
		if b.deps.Service == nil {
			b.initErr = errors.New("mail-merge.MailMergeBridge: missing Service in deps")
			return
		}
		store, err := NewStore(b.deps.Paths.Data)
		if err != nil {
			b.initErr = err
			return
		}
		b.store = store
	})
	return b.initErr
}

// getAttachmentDir returns the session's picked attachment folder.
func (b *MailMergeBridge) getAttachmentDir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attachmentDir
}

// emitRunUpdated notifies the frontend that a run's log changed.
func (b *MailMergeBridge) emitRunUpdated(runID string) {
	if b.deps.Ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(b.deps.Ctx, "mailmerge:run-updated", runID)
}

// effectiveAttachmentDir prefers the request's folder, falling back to
// the session's picked folder.
func (b *MailMergeBridge) effectiveAttachmentDir(req hostmm.RunRequest) string {
	if req.AttachmentDir != "" {
		return req.AttachmentDir
	}
	return b.getAttachmentDir()
}

// --- Wails-bound surface (MailMerge_*) ------------------------------------
//
// All methods gate on gateEnabled() first, per R16: disabled = zero value
// + nil error, no errors surfaced. ensureInit runs once per process;
// subsequent calls are the cost of one sync.Once check.

// MailMerge_ParseFile parses a CSV, JSON or XLSX source file into
// columns + rows. kind is "csv", "json" or "xlsx" (first sheet).
// PreviewRows holds the first 3 rows; Sheets holds the workbook's
// sheet names for XLSX (empty otherwise).
func (b *MailMergeBridge) MailMerge_ParseFile(path string, kind string) (hostmm.ParsedFile, error) {
	if !b.gateEnabled() {
		return hostmm.ParsedFile{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return hostmm.ParsedFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return hostmm.ParsedFile{}, fmt.Errorf("mail-merge: read file: %w", err)
	}
	var parsed *hostmm.ParsedData
	var sheets []string
	switch kind {
	case "csv":
		parsed, err = hostmm.ParseCSV(data, "")
	case "json":
		parsed, err = hostmm.ParseJSON(data)
	case "xlsx":
		parsed, err = hostmm.ParseXLSX(data, "")
		if err == nil {
			sheets, _ = hostmm.ListXLSXSheets(data)
		}
	default:
		return hostmm.ParsedFile{}, fmt.Errorf("mail-merge: unknown file kind %q", kind)
	}
	if err != nil {
		return hostmm.ParsedFile{}, err
	}
	preview := parsed.Rows
	if len(preview) > 3 {
		preview = preview[:3]
	}
	return hostmm.ParsedFile{Columns: parsed.Columns, RowCount: len(parsed.Rows), PreviewRows: preview, Sheets: sheets}, nil
}

// MailMerge_ParseFileFull parses a source file and returns ALL rows
// (not just the 3-row preview). XLSX uses this path — the frontend has
// no XLSX parser, so the backend returns the full table. sheet selects
// the workbook sheet by name ("" = first sheet; ignored for CSV/JSON).
func (b *MailMergeBridge) MailMerge_ParseFileFull(path string, kind string, sheet string) (hostmm.ParsedFileFull, error) {
	if !b.gateEnabled() {
		return hostmm.ParsedFileFull{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return hostmm.ParsedFileFull{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return hostmm.ParsedFileFull{}, fmt.Errorf("mail-merge: read file: %w", err)
	}
	var parsed *hostmm.ParsedData
	var sheets []string
	switch kind {
	case "csv":
		parsed, err = hostmm.ParseCSV(data, "")
	case "json":
		parsed, err = hostmm.ParseJSON(data)
	case "xlsx":
		parsed, err = hostmm.ParseXLSX(data, sheet)
		if err == nil {
			sheets, _ = hostmm.ListXLSXSheets(data)
		}
	default:
		return hostmm.ParsedFileFull{}, fmt.Errorf("mail-merge: unknown file kind %q", kind)
	}
	if err != nil {
		return hostmm.ParsedFileFull{}, err
	}
	if parsed.Rows == nil {
		parsed.Rows = []map[string]string{}
	}
	return hostmm.ParsedFileFull{Columns: parsed.Columns, Rows: parsed.Rows, Sheets: sheets, RowCount: len(parsed.Rows)}, nil
}

// MailMerge_GetContacts returns all unified contacts (local + CardDAV +
// Google + Microsoft) as merge rows. See ContactsResult for the return
// shape (Wails v2 cannot return rows, columns, error as three values).
func (b *MailMergeBridge) MailMerge_GetContacts() (ContactsResult, error) {
	if !b.gateEnabled() {
		return ContactsResult{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return ContactsResult{}, err
	}
	rows, cols, err := b.deps.Service.ListContacts()
	if err != nil {
		return ContactsResult{}, err
	}
	return ContactsResult{Rows: rows, Columns: cols}, nil
}

// MailMerge_Preview renders the template for the given row indices (empty
// indices = the first 3 rows) and returns the rendered messages.
func (b *MailMergeBridge) MailMerge_Preview(tmpl hostmm.Template, rows []map[string]string, indices []int) ([]hostmm.RenderedMessage, error) {
	if !b.gateEnabled() {
		return nil, nil
	}
	if err := b.ensureInit(); err != nil {
		return nil, err
	}
	if len(indices) == 0 {
		for i := 0; i < len(rows) && i < 3; i++ {
			indices = append(indices, i)
		}
	}
	dir := b.getAttachmentDir()
	out := make([]hostmm.RenderedMessage, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(rows) {
			return nil, fmt.Errorf("mail-merge: preview row %d out of range", idx)
		}
		msg, err := hostmm.RenderRow(tmpl, rows[idx], dir, idx)
		if err != nil {
			return nil, err
		}
		out = append(out, *msg)
	}
	return out, nil
}

// MailMerge_GetSigningInfo reports the account's signing setup for the
// merge dialog: whether a usable S/MIME certificate or PGP key exists
// for the sender identity, which kind would be used, and whether the
// composer's sign policy would sign by default. fromEmail is the
// template's From address (identity-specific lookup, else the account
// default); "" resolves against the account default.
func (b *MailMergeBridge) MailMerge_GetSigningInfo(accountID, fromEmail string) (hostmm.SigningInfo, error) {
	if !b.gateEnabled() {
		return hostmm.SigningInfo{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return hostmm.SigningInfo{}, err
	}
	return b.deps.Service.SigningInfo(accountID, fromEmail), nil
}

// MailMerge_Validate checks a template + rows before a run. Any
// unresolved variable BLOCKS the run; invalid-email, empty-To and
// missing-attachment rows are skipped and logged, not run-blocking.
// When the template requests signing, the account's signing setup is
// trial-signed here too: a missing or unusable key/certificate sets
// SigningError, which blocks the run the same way — the failure must
// be visible before the run starts, not discovered mid-run.
func (b *MailMergeBridge) MailMerge_Validate(tmpl hostmm.Template, rows []map[string]string) (hostmm.ValidationResult, error) {
	if !b.gateEnabled() {
		return hostmm.ValidationResult{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return hostmm.ValidationResult{}, err
	}
	out := hostmm.Validate(tmpl, rows, b.getAttachmentDir())
	if tmpl.Sign {
		if _, err := b.deps.Service.CheckSigningForRun(hostmm.RunRequest{Template: tmpl, Rows: rows, AccountID: tmpl.AccountID}); err != nil {
			out.SigningError = err.Error()
		}
	}
	return out, nil
}

// MailMerge_StartRun starts a run asynchronously (goroutine) and returns
// its run ID immediately. Any unresolved variable blocks the start, as
// does a signing request the account cannot satisfy (no usable
// certificate/key — checked here, before the run is created).
// Rows whose To resolves empty after rendering are skipped + logged; a
// To that still contains {{ after rendering is never sent. Duplicates
// (same To address twice) are suppressed with a count shown.
//
// Deliver modes: "drafts" (save per-recipient drafts), "send" (send
// now, safety cap applies), "later" (Phase 2: render each row into
// the persisted outbox queue — nothing is sent by the run itself;
// the background runner or Send Unsent dispatches the queue).
func (b *MailMergeBridge) MailMerge_StartRun(req hostmm.RunRequest) (string, error) {
	if !b.gateEnabled() {
		return "", nil
	}
	if err := b.ensureInit(); err != nil {
		return "", err
	}
	req.AttachmentDir = b.effectiveAttachmentDir(req)
	selected, _ := hostmm.SliceRows(req.Rows, req.StartRow, req.StopRow)
	if v := hostmm.Validate(req.Template, selected, req.AttachmentDir); len(v.UnresolvedVariables) > 0 {
		return "", fmt.Errorf("mail-merge: unresolved variable(s): %s", strings.Join(v.UnresolvedVariables, ", "))
	}
	// A signing run proves the key is usable before the run is even
	// created (the service pre-flights again at execution, so a key
	// that vanishes between here and launch still fails before any
	// row is delivered). The resolved kind is stamped onto the stored
	// request so resume/batch runs reuse it.
	if req.Template.Sign {
		kind, err := b.deps.Service.CheckSigningForRun(req)
		if err != nil {
			return "", fmt.Errorf("mail-merge: signing unavailable: %w", err)
		}
		req.Template.SignKind = kind
	}
	if req.DeliverMode != "drafts" && req.DeliverMode != "send" && req.DeliverMode != "later" {
		return "", fmt.Errorf("mail-merge: unknown deliver mode %q", req.DeliverMode)
	}
	runID := uuid.NewString()
	run := &hostmm.Run{
		ID:          runID,
		CreatedAt:   time.Now().UTC(),
		AccountID:   req.AccountID,
		DeliverMode: req.DeliverMode,
		Status:      hostmm.RunRunning,
		TotalRows:   len(selected),
		Rows:        []hostmm.RowResult{},
	}
	if err := b.store.SaveRun(run, req); err != nil {
		return "", err
	}
	if req.DeliverMode == "later" {
		b.launchQueueExecution(run, req, nil)
	} else {
		b.launchExecution(run, req, nil)
	}
	return runID, nil
}

// MailMerge_GetRun returns one run with its per-row log.
func (b *MailMergeBridge) MailMerge_GetRun(runID string) (hostmm.Run, error) {
	if !b.gateEnabled() {
		return hostmm.Run{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return hostmm.Run{}, err
	}
	run, err := b.store.GetRun(runID)
	if err != nil {
		return hostmm.Run{}, err
	}
	if run == nil {
		return hostmm.Run{}, fmt.Errorf("mail-merge: run %q not found", runID)
	}
	return *run, nil
}

// MailMerge_ListRuns returns run summaries, newest first.
func (b *MailMergeBridge) MailMerge_ListRuns() ([]hostmm.RunSummary, error) {
	if !b.gateEnabled() {
		return nil, nil
	}
	if err := b.ensureInit(); err != nil {
		return nil, err
	}
	return b.store.ListRuns()
}

// MailMerge_CancelRun cancels a running run. It stops after the current
// message; unprocessed rows are recorded as skipped. Cancelling a
// finished run is a no-op (nil error); an unknown run ID errors.
func (b *MailMergeBridge) MailMerge_CancelRun(runID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	b.mu.Lock()
	ar, ok := b.active[runID]
	b.mu.Unlock()
	if ok {
		ar.cancel()
		return nil
	}
	run, err := b.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("mail-merge: run %q not found", runID)
	}
	return nil
}

// MailMerge_ResumeRun re-runs the FAILED rows of a run only (NOT held
// rows — held rows need explicit batch approval; NOT already
// sent/draft-saved rows). A run that failed as a whole (status
// "failed") resumes all of its rows — none of them was ever
// processed. Returns the new run's ID.
func (b *MailMergeBridge) MailMerge_ResumeRun(runID string) (string, error) {
	if !b.gateEnabled() {
		return "", nil
	}
	if err := b.ensureInit(); err != nil {
		return "", err
	}
	if b.isActive(runID) {
		return "", fmt.Errorf("mail-merge: run %q is still running", runID)
	}
	run, err := b.store.GetRun(runID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", fmt.Errorf("mail-merge: run %q not found", runID)
	}
	req, err := b.store.GetRunRequest(runID)
	if err != nil {
		return "", err
	}
	var rows []map[string]string
	var messageIDs []string
	for _, r := range run.Rows {
		if r.Status == hostmm.RowFailed && r.Index >= 0 && r.Index < len(req.Rows) {
			rows = append(rows, req.Rows[r.Index])
			// The Message-ID assigned when this row was first
			// rendered rides along, parallel to rows, so the retry
			// rebuilds the same mail with the same identity.
			messageIDs = append(messageIDs, r.MessageID)
		}
	}
	// A run that failed as a whole (RunFailed) with no failed rows
	// recorded — e.g. the failure predates audit 16's row recording
	// — resumes its full stored request: none of its rows was ever
	// processed, so all of them are the failed set.
	if len(rows) == 0 && run.Status == hostmm.RunFailed {
		rows = append(rows, req.Rows...)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("mail-merge: run %q has no failed rows to resume", runID)
	}
	req.Rows = rows
	req.MessageIDs = messageIDs
	req.StartRow = 0
	req.StopRow = 0
	req.AttachmentDir = b.effectiveAttachmentDir(req)
	if v := hostmm.Validate(req.Template, req.Rows, req.AttachmentDir); len(v.UnresolvedVariables) > 0 {
		return "", fmt.Errorf("mail-merge: unresolved variable(s): %s", strings.Join(v.UnresolvedVariables, ", "))
	}
	newID := uuid.NewString()
	newRun := &hostmm.Run{
		ID:          newID,
		CreatedAt:   time.Now().UTC(),
		AccountID:   req.AccountID,
		DeliverMode: req.DeliverMode,
		Status:      hostmm.RunRunning,
		TotalRows:   len(rows),
		Rows:        []hostmm.RowResult{},
	}
	if err := b.store.SaveRun(newRun, req); err != nil {
		return "", err
	}
	if req.DeliverMode == "later" {
		b.launchQueueExecution(newRun, req, nil)
	} else {
		b.launchExecution(newRun, req, nil)
	}
	return newID, nil
}

// MailMerge_SendNextBatch sends the next CapLimit held rows of a run
// (cap-held rows only) and leaves the rest held. Runs asynchronously;
// progress lands in the same run's log.
func (b *MailMergeBridge) MailMerge_SendNextBatch(runID string) error {
	return b.sendBatch(runID, true)
}

// MailMerge_SendAllRemaining sends all held rows of a run, ignoring the
// cap (explicit user approval). Runs asynchronously.
func (b *MailMergeBridge) MailMerge_SendAllRemaining(runID string) error {
	return b.sendBatch(runID, false)
}

// MailMerge_CancelRemaining discards all held rows of a run, marking
// them skipped. Synchronous.
func (b *MailMergeBridge) MailMerge_CancelRemaining(runID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if b.isActive(runID) {
		return fmt.Errorf("mail-merge: run %q is still running", runID)
	}
	run, err := b.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("mail-merge: run %q not found", runID)
	}
	held := 0
	for _, r := range run.Rows {
		if r.Status == hostmm.RowHeld {
			held++
			r.Status = hostmm.RowSkipped
			r.Error = "cancelled by user — held rows discarded"
			if err := b.store.SaveRowResult(runID, r); err != nil {
				return err
			}
		}
	}
	if held == 0 {
		return fmt.Errorf("mail-merge: run %q has no held rows", runID)
	}
	updated, err := b.store.GetRun(runID)
	if err != nil {
		return err
	}
	updated.RecomputeCounts()
	updated.Status = hostmm.RunCancelled
	if err := b.store.UpdateRunStatusAndCounts(updated); err != nil {
		return err
	}
	b.emitRunUpdated(runID)
	return nil
}

// MailMerge_GetSettings returns one account's settings over the defaults.
func (b *MailMergeBridge) MailMerge_GetSettings(accountID string) (Settings, error) {
	if !b.gateEnabled() {
		return Settings{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return Settings{}, err
	}
	return b.store.GetSettings(accountID)
}

// MailMerge_SaveSettings validates and persists one account's settings.
func (b *MailMergeBridge) MailMerge_SaveSettings(accountID string, st Settings) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if err := st.Validate(); err != nil {
		return err
	}
	return b.store.SaveSettings(accountID, st)
}

// MailMerge_PickFile opens a file dialog for a CSV or JSON source file
// and returns the picked path ("" when the user cancels).
func (b *MailMergeBridge) MailMerge_PickFile(kind string) (string, error) {
	if !b.gateEnabled() {
		return "", nil
	}
	if b.deps.Ctx == nil {
		return "", fmt.Errorf("mail-merge: no UI context for file dialog")
	}
	var filter wailsRuntime.FileFilter
	switch kind {
	case "csv":
		filter = wailsRuntime.FileFilter{DisplayName: "CSV Files (*.csv)", Pattern: "*.csv"}
	case "json":
		filter = wailsRuntime.FileFilter{DisplayName: "JSON Files (*.json)", Pattern: "*.json"}
	case "xlsx":
		filter = wailsRuntime.FileFilter{DisplayName: "Excel Files (*.xlsx)", Pattern: "*.xlsx"}
	default:
		return "", fmt.Errorf("mail-merge: unknown file kind %q", kind)
	}
	path, err := wailsRuntime.OpenFileDialog(b.deps.Ctx, wailsRuntime.OpenDialogOptions{
		Title:   "Select Mail Merge Source File",
		Filters: []wailsRuntime.FileFilter{filter},
	})
	if err != nil {
		return "", fmt.Errorf("mail-merge: open file dialog: %w", err)
	}
	return path, nil
}

// MailMerge_PickAttachmentDir opens a folder dialog for the folder
// per-row attachment filenames resolve against, and remembers it for
// this session (Validate/Preview/StartRun use it when the request
// carries no folder).
func (b *MailMergeBridge) MailMerge_PickAttachmentDir() (string, error) {
	if !b.gateEnabled() {
		return "", nil
	}
	if b.deps.Ctx == nil {
		return "", fmt.Errorf("mail-merge: no UI context for folder dialog")
	}
	path, err := wailsRuntime.OpenDirectoryDialog(b.deps.Ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Attachment Folder",
	})
	if err != nil {
		return "", fmt.Errorf("mail-merge: open folder dialog: %w", err)
	}
	if path != "" {
		b.mu.Lock()
		b.attachmentDir = path
		b.mu.Unlock()
	}
	return path, nil
}

// --- run execution --------------------------------------------------------

// isActive reports whether a run is currently executing.
func (b *MailMergeBridge) isActive(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.active[runID]
	return ok
}

// launchExecution runs req in a goroutine, persisting each row result as
// it arrives and the final status on completion. indexMap translates a
// RowResult.Index from the executed request's row positions back to the
// persisted run's row indices (nil = identity, for fresh runs).
//
// The passed run's counts are the live tally baseline: for a fresh run
// they are zero; for a batch continuation the caller subtracts the held
// rows about to be re-processed (they are re-counted with their new
// status as results land). Completion reloads the run from the store and
// recomputes counts from the persisted rows, so the final tally is
// always exact even if a batch is cancelled mid-way.
func (b *MailMergeBridge) launchExecution(run *hostmm.Run, req hostmm.RunRequest, indexMap []int) {
	ctx, cancel := context.WithCancel(context.Background())
	ar := &activeRun{cancel: cancel, done: make(chan struct{})}
	b.mu.Lock()
	b.active[run.ID] = ar
	b.mu.Unlock()

	go func() {
		defer func() {
			b.mu.Lock()
			delete(b.active, run.ID)
			b.mu.Unlock()
			close(ar.done)
		}()
		// deactivate removes the run from the in-flight map BEFORE
		// the goroutine returns, so a caller that just observed the
		// terminal status in the DB never sees a stale "still
		// running" (the deferred delete is the safety net).
		deactivate := func() {
			b.mu.Lock()
			delete(b.active, run.ID)
			b.mu.Unlock()
		}
		// persistErr records the first row-result persist failure
		// (if any) so it can be surfaced on the run at completion
		// instead of silently dropping the row. Written and read
		// only by this goroutine.
		var persistErr error
		outcome, err := b.deps.Service.Execute(ctx, req, func(r hostmm.RowResult) {
			if indexMap != nil {
				if r.Index < 0 || r.Index >= len(indexMap) {
					return
				}
				r.Index = indexMap[r.Index]
			}
			// Persist the row, then refresh the live tally. A
			// persist failure must not silently drop the row
			// result (a sent row would vanish from the log, a
			// failed row would become unresumable): retry once,
			// then record the failure to surface it on the run at
			// completion. The in-memory tally still advances, so
			// the live counts stay right.
			if err := b.store.SaveRowResult(run.ID, r); err != nil {
				if err2 := b.store.SaveRowResult(run.ID, r); err2 != nil && persistErr == nil {
					persistErr = fmt.Errorf("mail-merge: persist row %d result: %w", r.Index, err2)
				}
			}
			b.mu.Lock()
			switch r.Status {
			case hostmm.RowSent, hostmm.RowDraftSaved:
				run.SentCount++
			case hostmm.RowFailed:
				run.FailedCount++
			case hostmm.RowSkipped:
				run.SkippedCount++
			case hostmm.RowHeld:
				run.HeldCount++
			}
			b.mu.Unlock()
			_ = b.store.UpdateRunStatusAndCounts(run)
			b.emitRunUpdated(run.ID)
		})
		if err != nil {
			// The whole run failed before/without per-row results
			// (e.g. SMTP connect failed). It is recorded as failed,
			// not cancelled: the error is stored on the run and
			// every row the run never processed is recorded
			// RowFailed, so ResumeRun can re-run it.
			if recErr := b.failWholeRun(run, req, indexMap, err); recErr != nil {
				slog := logging.WithComponent("mailmerge")
				slog.Warn().Err(recErr).Msg("Mail Merge whole-run failure: failed rows could not be recorded")
			}
			deactivate()
			return
		}
		// Final tally from the persisted rows (source of truth).
		final, ferr := b.store.GetRun(run.ID)
		if ferr != nil || final == nil {
			return
		}
		final.RecomputeCounts()
		switch {
		case outcome.Cancelled:
			final.Status = hostmm.RunCancelled
		case final.HeldCount > 0:
			final.Status = hostmm.RunHeld
		default:
			final.Status = hostmm.RunDone
		}
		// A row result that could not be persisted is surfaced on
		// the run (the terminal status stands — the rows were
		// delivered, only the log is incomplete).
		if persistErr != nil {
			final.Error = persistErr.Error() + " (the run log may be missing rows)"
		}
		_ = b.store.UpdateRunStatusAndCounts(final)
		b.emitRunUpdated(run.ID)
		deactivate()
	}()
}

// failWholeRun records a whole-run failure (audit 16): the run
// becomes RunFailed with the error stored on it, and every row the
// run never processed is recorded RowFailed with that error, so
// ResumeRun can re-run the run. Rows already in the log (a failure
// after partial progress) are left alone. indexMap translates the
// executed request's row positions back to the persisted run's row
// indices (nil = identity, for fresh runs).
//
// It returns the row-recording error, if saving the failed rows
// themselves failed: the run is still marked failed, and the failure
// is appended to the run's error text, so the user sees the log is
// incomplete instead of silently losing the failed rows. A failed
// run with no recorded failed rows is still resumable — ResumeRun
// re-runs the whole stored request then — and the Resume button is
// gated on the failed status, not on the failed count.
func (b *MailMergeBridge) failWholeRun(run *hostmm.Run, req hostmm.RunRequest, indexMap []int, runErr error) error {
	_, indices := hostmm.SliceRows(req.Rows, req.StartRow, req.StopRow)
	logged := map[int]bool{}
	if existing, err := b.store.GetRun(run.ID); err == nil && existing != nil {
		for _, r := range existing.Rows {
			logged[r.Index] = true
		}
	}
	now := time.Now()
	var recordErr error
	unrecorded := 0
	for pos, origIndex := range indices {
		idx := origIndex
		if indexMap != nil {
			if pos < 0 || pos >= len(indexMap) {
				continue
			}
			idx = indexMap[pos]
		}
		if logged[idx] {
			continue
		}
		if err := b.store.SaveRowResult(run.ID, hostmm.RowResult{
			Index: idx, Status: hostmm.RowFailed,
			Error: runErr.Error(), Timestamp: now,
		}); err != nil {
			unrecorded++
			if recordErr == nil {
				recordErr = err
			}
		}
	}
	final, ferr := b.store.GetRun(run.ID)
	if ferr != nil || final == nil {
		if recordErr != nil {
			return recordErr
		}
		return ferr
	}
	final.RecomputeCounts()
	final.Status = hostmm.RunFailed
	final.Error = runErr.Error()
	if recordErr != nil {
		final.Error += fmt.Sprintf(" (could not record %d failed row(s): %v; Resume re-runs the whole run)", unrecorded, recordErr)
	}
	_ = b.store.UpdateRunStatusAndCounts(final)
	b.emitRunUpdated(run.ID)
	return recordErr
}

// sendBatch is the shared implementation of SendNextBatch (capped) and
// SendAllRemaining (uncapped). It executes the held rows as a
// continuation of the same run: progress updates the original run's rows
// in place.
func (b *MailMergeBridge) sendBatch(runID string, capped bool) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if b.isActive(runID) {
		return fmt.Errorf("mail-merge: run %q is still running", runID)
	}
	run, err := b.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("mail-merge: run %q not found", runID)
	}
	if run.DeliverMode != "send" {
		return fmt.Errorf("mail-merge: run %q is not a send run", runID)
	}
	req, err := b.store.GetRunRequest(runID)
	if err != nil {
		return err
	}
	var heldIndices []int
	for _, r := range run.Rows {
		if r.Status == hostmm.RowHeld {
			heldIndices = append(heldIndices, r.Index)
		}
	}
	if len(heldIndices) == 0 {
		return fmt.Errorf("mail-merge: run %q has no held rows", runID)
	}
	take := heldIndices
	limit := req.CapLimit
	if limit <= 0 {
		limit = 100
	}
	if capped && len(take) > limit {
		take = take[:limit]
	}
	rows := make([]map[string]string, 0, len(take))
	messageIDs := make([]string, 0, len(take))
	heldIDs := map[int]string{}
	for _, r := range run.Rows {
		if r.Status == hostmm.RowHeld {
			heldIDs[r.Index] = r.MessageID
		}
	}
	for _, idx := range take {
		if idx < 0 || idx >= len(req.Rows) {
			return fmt.Errorf("mail-merge: run %q has a held row %d out of range", runID, idx)
		}
		rows = append(rows, req.Rows[idx])
		// The Message-ID assigned when this row was first rendered
		// rides along, parallel to rows, so the batch rebuilds the
		// same mail with the same identity.
		messageIDs = append(messageIDs, heldIDs[idx])
	}
	sub := hostmm.RunRequest{
		Template:      req.Template,
		Rows:          rows,
		MessageIDs:    messageIDs,
		SourceKind:    req.SourceKind,
		DeliverMode:   "send",
		PauseSeconds:  req.PauseSeconds,
		RandomPause:   req.RandomPause,
		CapEnabled:    capped,
		CapLimit:      limit,
		AccountID:     req.AccountID,
		AttachmentDir: b.effectiveAttachmentDir(req),
	}
	// The sub-run executes against `rows`; indexMap translates its
	// positions back to the original run's row indices. The live tally
	// starts from the current counts minus the held rows about to be
	// re-processed (they are re-counted with their new status).
	run.RecomputeCounts()
	run.HeldCount -= len(take)
	run.Status = hostmm.RunRunning
	if err := b.store.UpdateRunStatusAndCounts(run); err != nil {
		return err
	}
	b.launchExecution(run, sub, take)
	return nil
}

// launchQueueExecution runs a "Send later" run in a goroutine: each
// row is rendered into its final RFC822 bytes (Service.RenderForQueue)
// and enqueued in the persisted outbox queue; the run's rows are
// recorded as "queued". Nothing is sent here — the background runner
// (main process only) or Send Unsent dispatches the queue. Mirrors
// launchExecution's bookkeeping (active-run map, live tally,
// terminal status); indexMap translates executed positions back to
// the persisted run's row indices (nil = identity, for fresh runs).
func (b *MailMergeBridge) launchQueueExecution(run *hostmm.Run, req hostmm.RunRequest, indexMap []int) {
	ctx, cancel := context.WithCancel(context.Background())
	ar := &activeRun{cancel: cancel, done: make(chan struct{})}
	b.mu.Lock()
	b.active[run.ID] = ar
	b.mu.Unlock()

	go func() {
		defer func() {
			b.mu.Lock()
			delete(b.active, run.ID)
			b.mu.Unlock()
			close(ar.done)
		}()
		deactivate := func() {
			b.mu.Lock()
			delete(b.active, run.ID)
			b.mu.Unlock()
		}
		now := b.queueNow()
		// persistErr records the first row-result persist failure
		// (if any) so it can be surfaced on the run at completion
		// instead of silently dropping the row. Written and read
		// only by this goroutine.
		var persistErr error
		outcome, err := b.deps.Service.RenderForQueue(ctx, req,
			func(qm hostmm.QueuedMessage) error {
				if indexMap != nil {
					if qm.RowIndex < 0 || qm.RowIndex >= len(indexMap) {
						return nil
					}
					qm.RowIndex = indexMap[qm.RowIndex]
				}
				item := QueuedItem{
					ID:            uuid.NewString(),
					RunID:         run.ID,
					RowIndex:      qm.RowIndex,
					AccountID:     req.AccountID,
					Recipient:     qm.Email,
					Subject:       qm.Subject,
					FromAddress:   qm.From,
					To:            qm.To,
					RawMessage:    qm.Raw,
					Status:        QueueStatusQueued,
					Attempts:      0,
					MaxAttempts:   b.queueMaxAttempts(),
					NextAttemptAt: now,
					CreatedAt:     now,
				}
				// An enqueue failure is returned so the service
				// records the row failed (not queued) and does not
				// mark the address seen — see finding 17.
				return b.store.EnqueueItem(item)
			},
			func(r hostmm.RowResult) {
				if indexMap != nil {
					if r.Index < 0 || r.Index >= len(indexMap) {
						return
					}
					r.Index = indexMap[r.Index]
				}
				// Persist the row, then refresh the live tally. A
				// persist failure must not silently drop the row
				// result: retry once, then record the failure to
				// surface it on the run at completion (same
				// contract as launchExecution).
				if err := b.store.SaveRowResult(run.ID, r); err != nil {
					if err2 := b.store.SaveRowResult(run.ID, r); err2 != nil && persistErr == nil {
						persistErr = fmt.Errorf("mail-merge: persist row %d result: %w", r.Index, err2)
					}
				}
				b.mu.Lock()
				switch r.Status {
				case hostmm.RowSent, hostmm.RowDraftSaved:
					run.SentCount++
				case hostmm.RowFailed:
					run.FailedCount++
				case hostmm.RowSkipped:
					run.SkippedCount++
				case hostmm.RowHeld:
					run.HeldCount++
				case hostmm.RowQueued:
					run.QueuedCount++
				}
				b.mu.Unlock()
				_ = b.store.UpdateRunStatusAndCounts(run)
				b.emitRunUpdated(run.ID)
			})
		if err != nil {
			// The whole run failed before/without per-row results
			// (e.g. a bad base64 global attachment) — same contract
			// as launchExecution: mark failed, record the
			// unprocessed rows failed, keep the error on the run.
			if recErr := b.failWholeRun(run, req, indexMap, err); recErr != nil {
				slog := logging.WithComponent("mailmerge")
				slog.Warn().Err(recErr).Msg("Mail Merge whole-run failure: failed rows could not be recorded")
			}
			deactivate()
			return
		}
		final, ferr := b.store.GetRun(run.ID)
		if ferr != nil || final == nil {
			return
		}
		final.RecomputeCounts()
		switch {
		case outcome.Cancelled:
			final.Status = hostmm.RunCancelled
		case final.QueuedCount > 0:
			final.Status = hostmm.RunQueued
		default:
			final.Status = hostmm.RunDone
		}
		// A row result that could not be persisted is surfaced on
		// the run (the terminal status stands — the rows were
		// queued, only the log is incomplete).
		if persistErr != nil {
			final.Error = persistErr.Error() + " (the run log may be missing rows)"
		}
		_ = b.store.UpdateRunStatusAndCounts(final)
		b.emitRunUpdated(run.ID)
		b.emitQueueUpdated()
		deactivate()
	}()
}
