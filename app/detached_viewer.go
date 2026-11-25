package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	goSync "sync"
	"time"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/certificate"
	"github.com/absonsnet/daak/internal/contact"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/draft"
	"github.com/absonsnet/daak/internal/email"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/imap"
	"github.com/absonsnet/daak/internal/ipc"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/oauth2"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/platform"
	"github.com/absonsnet/daak/internal/reaction"
	"github.com/absonsnet/daak/internal/sendqueue"
	"github.com/absonsnet/daak/internal/settings"
	"github.com/absonsnet/daak/internal/smime"
	"github.com/absonsnet/daak/internal/smtp"
	syncEngine "github.com/absonsnet/daak/internal/sync"
	goImap "github.com/emersion/go-imap/v2"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ViewerConfig holds the configuration for a message-viewer popup window.
type ViewerConfig struct {
	AccountID  string // Required: account owning the thread
	FolderID   string // Required: folder containing the thread
	ThreadID   string // Required: thread to display
	IPCAddress string // Required: address of main window's IPC server
}

// ViewMode is the thread a viewer popup was opened for, returned to the
// frontend so it knows what to render.
type ViewMode struct {
	AccountID  string `json:"accountId"`
	FolderID   string `json:"folderId"`
	FolderType string `json:"folderType"`
	ThreadID   string `json:"threadId"`
}

// ViewerApp is a lightweight app struct for detached message-viewer popup
// windows, mirroring ComposerApp. It connects to the main window via IPC
// and shares the same database, so the popup renders the same
// ConversationViewer as the main window (through the viewerBindings
// indirection) with the same read/action methods bound under its own
// name.
//
// Differences from the main window, by design:
//   - There is no undo stack in this process: Undo() reports nothing to
//     undo. Archive/Trash/Spam/Delete move/delete locally first and sync
//     to IMAP in the background, but the action cannot be undone from the
//     popup (the main window's undo stack never sees it).
//   - Reply/forward/edit-draft do not open a composer inside the popup:
//     they spawn a detached composer window (the same --compose process
//     the main window spawns), which owns the full send path.
//   - A destructive action (archive/trash/spam/delete) closes the popup
//     via the frontend's onActionComplete handler — the thread is gone
//     from its folder, so there is nothing left to show.
type ViewerApp struct {
	ctx    context.Context
	config ViewerConfig

	// IPC client for communication with main window
	ipcClient ipc.Client
	ipcToken  string

	// Database (shared with main window)
	db                  *database.DB
	accountStore        *account.Store
	folderStore         *folder.Store
	messageStore        *message.Store
	attachmentStore     *message.AttachmentStore
	reactionStore       *reaction.Store
	settingsStore       *settings.Store
	imageAllowlistStore *settings.ImageAllowlistStore
	contactStore        *contact.Store
	draftStore          *draft.Store
	credStore           *credentials.Store
	certStore           *certificate.Store

	// Outbox queue (reactions only — the viewer never composes, and
	// never runs the dispatch worker; the main process owns it).
	sendQueue *sendQueueService

	// S/MIME and PGP support (on-view decryption/verification)
	smimeStore     *smime.Store
	smimeSigner    *smime.Signer
	smimeVerifier  *smime.Verifier
	smimeEncryptor *smime.Encryptor
	smimeDecryptor *smime.Decryptor
	pgpStore       *pgp.Store
	pgpSigner      *pgp.Signer
	pgpVerifier    *pgp.Verifier
	pgpEncryptor   *pgp.Encryptor
	pgpDecryptor   *pgp.Decryptor

	// IMAP pool for flag/move/delete sync and on-demand body fetch
	imapPool *imap.Pool

	// OAuth2 manager for token refresh
	oauth2Manager *oauth2.Manager

	// Shared draft/compose operations (reactions send through these)
	draftOps   draftOps
	composeOps composeOps

	// Sync engine for on-demand body fetch, raw source and decrypted-body
	// parsing. Constructed, never started: no background sync runs here.
	syncEngine *syncEngine.Engine

	// Paths
	paths *platform.Paths

	// closeConfirmed is set once the user confirmed the close, so
	// BeforeClose lets the programmatic close through.
	closeConfirmed bool

	// Paths the user chose in a save dialog this session, for OpenFile /
	// OpenFolder validation (#474, mirrors App).
	recentSavePaths map[string]struct{}
	recentSaveMu    goSync.Mutex
}

// NewViewerApp creates a new ViewerApp with the given configuration.
func NewViewerApp(config ViewerConfig) *ViewerApp {
	return &ViewerApp{
		config: config,
	}
}

// Startup is called when the viewer popup window starts.
func (v *ViewerApp) Startup(ctx context.Context) {
	v.ctx = ctx

	log := logging.WithComponent("viewer")

	log.Info().
		Str("accountID", v.config.AccountID).
		Str("folderID", v.config.FolderID).
		Str("threadID", v.config.ThreadID).
		Str("ipcAddress", v.config.IPCAddress).
		Msg("Viewer popup starting")

	paths, err := platform.GetPaths()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get platform paths")
	}
	v.paths = paths

	db, err := database.Open(paths.DatabasePath())
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	v.db = db

	v.accountStore = account.NewStore(db)
	v.folderStore = folder.NewStore(db)
	v.messageStore = message.NewStore(db)
	v.attachmentStore = message.NewAttachmentStore(db)
	v.reactionStore = reaction.NewStore(db)
	v.settingsStore = settings.NewStore(db)
	v.imageAllowlistStore = settings.NewImageAllowlistStore(db)
	v.contactStore = contact.NewStore(db.DB)
	v.draftStore = draft.NewStore(db)

	credStore, err := credentials.NewStore(db.DB, paths.Data)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize credential store")
	}
	v.credStore = credStore

	v.certStore = certificate.NewStore(db.DB)

	v.smimeStore = smime.NewStore(db.DB, log)
	v.smimeSigner = smime.NewSigner(v.smimeStore, credStore, log)
	v.smimeVerifier = smime.NewVerifier(v.smimeStore, log)
	v.smimeEncryptor = smime.NewEncryptor(v.smimeStore, credStore, log)
	v.smimeDecryptor = smime.NewDecryptor(v.smimeStore, credStore, log)

	v.pgpStore = pgp.NewStore(db.DB, log)
	v.pgpSigner = pgp.NewSigner(v.pgpStore, credStore, log)
	v.pgpVerifier = pgp.NewVerifier(v.pgpStore, log)
	v.pgpEncryptor = pgp.NewEncryptor(v.pgpStore, credStore, log)
	v.pgpDecryptor = pgp.NewDecryptor(v.pgpStore, credStore, log)

	poolConfig := imap.DefaultPoolConfig()
	poolConfig.MaxConnections = 1 // Viewer only needs 1 connection
	v.imapPool = imap.NewPool(poolConfig, v.getIMAPCredentials)

	v.oauth2Manager = oauth2.NewManager()

	v.draftOps = draftOps{
		accountStore:   v.accountStore,
		folderStore:    v.folderStore,
		messageStore:   v.messageStore,
		draftStore:     v.draftStore,
		imapPool:       v.imapPool,
		smimeSigner:    v.smimeSigner,
		smimeEncryptor: v.smimeEncryptor,
		smimeDecryptor: v.smimeDecryptor,
		pgpSigner:      v.pgpSigner,
		pgpEncryptor:   v.pgpEncryptor,
		pgpDecryptor:   v.pgpDecryptor,
	}
	v.composeOps = composeOps{
		accountStore:   v.accountStore,
		folderStore:    v.folderStore,
		credStore:      v.credStore,
		certStore:      v.certStore,
		contactStore:   v.contactStore,
		oauth2Manager:  v.oauth2Manager,
		smimeStore:     v.smimeStore,
		smimeSigner:    v.smimeSigner,
		smimeEncryptor: v.smimeEncryptor,
		pgpStore:       v.pgpStore,
		pgpSigner:      v.pgpSigner,
		pgpEncryptor:   v.pgpEncryptor,
		draftOps:       &v.draftOps,
	}

	v.syncEngine = syncEngine.NewEngine(v.imapPool, v.accountStore, v.folderStore, v.messageStore, v.attachmentStore)
	v.syncEngine.SetSMIMEVerifier(v.smimeVerifier)
	v.syncEngine.SetPGPVerifier(v.pgpVerifier)
	v.syncEngine.SetReactionStore(v.reactionStore)

	// Reactions queue through the shared Outbox like the main window;
	// this process never dispatches (no worker) and never shows the
	// Undo toast itself — the main window owns both, via IPC.
	v.sendQueue = &sendQueueService{
		store:      sendqueue.NewStore(db),
		settings:   v.settingsStore,
		ops:        &v.composeOps,
		draftStore: v.draftStore,
		ctx:        v.ctx,
	}

	if err := v.connectIPC(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to connect to IPC server")
		// Continue anyway - viewer can still read from the shared DB
	}

	v.notifyReady()

	log.Info().Msg("Viewer popup started successfully")
}

// Shutdown is called when the viewer popup window is closing.
func (v *ViewerApp) Shutdown(ctx context.Context) {
	log := logging.WithComponent("viewer")

	v.notifyClosed()

	if v.ipcClient != nil {
		v.ipcClient.Close()
	}
	if v.imapPool != nil {
		v.imapPool.CloseAll()
	}
	if v.db != nil {
		v.db.Close()
	}

	log.Info().Msg("Viewer popup shutdown complete")
}

// connectIPC establishes connection to the main window's IPC server.
func (v *ViewerApp) connectIPC(ctx context.Context) error {
	log := logging.WithComponent("viewer.ipc")

	tokenBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read token from stdin: %w", err)
	}
	v.ipcToken = strings.TrimSpace(string(tokenBytes))

	if v.ipcToken == "" {
		return fmt.Errorf("no token provided via stdin")
	}

	log.Debug().Str("address", v.config.IPCAddress).Msg("Connecting to IPC server")

	v.ipcClient = ipc.NewClient(v.config.IPCAddress)
	v.ipcClient.OnMessage(v.handleIPCMessage)

	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := v.ipcClient.Connect(connectCtx, v.ipcToken); err != nil {
		return fmt.Errorf("failed to connect to IPC server: %w", err)
	}

	log.Info().Msg("Connected to IPC server")
	return nil
}

// handleIPCMessage processes messages received from the main window.
func (v *ViewerApp) handleIPCMessage(msg ipc.Message) {
	log := logging.WithComponent("viewer.ipc")

	log.Debug().Str("type", msg.Type).Msg("Received IPC message")

	switch msg.Type {
	case ipc.TypeThemeChanged:
		var payload ipc.ThemeChangedPayload
		if err := msg.ParsePayload(&payload); err == nil {
			wailsRuntime.EventsEmit(v.ctx, "theme:changed", payload.Theme)
		}

	case ipc.TypeShutdown:
		var payload ipc.ShutdownPayload
		_ = msg.ParsePayload(&payload)
		log.Info().Str("reason", payload.Reason).Msg("Received shutdown request from main window")
		wailsRuntime.EventsEmit(v.ctx, "app:shutdown", payload.Reason)

	default:
		log.Debug().Str("type", msg.Type).Msg("Unknown IPC message type")
	}
}

// notifyReady sends a ready notification to the main window.
func (v *ViewerApp) notifyReady() {
	if v.ipcClient == nil {
		return
	}
	msg, err := ipc.NewMessage(ipc.TypeViewerReady, nil)
	if err != nil {
		return
	}
	_ = v.ipcClient.Send(msg)
}

// notifyClosed sends a closed notification to the main window.
func (v *ViewerApp) notifyClosed() {
	if v.ipcClient == nil {
		return
	}
	msg, err := ipc.NewMessage(ipc.TypeViewerClosed, nil)
	if err != nil {
		return
	}
	_ = v.ipcClient.Send(msg)
}

// notifySwitch tells the main window to open this thread in another mode
// (tab or dialog). The popup closes itself right after sending.
func (v *ViewerApp) notifySwitch(mode string) {
	if v.ipcClient == nil {
		return
	}
	msg, err := ipc.NewMessage(ipc.TypeViewerSwitch, ipc.ViewerSwitchPayload{
		Mode:      mode,
		AccountID: v.config.AccountID,
		FolderID:  v.config.FolderID,
		ThreadID:  v.config.ThreadID,
	})
	if err != nil {
		return
	}
	_ = v.ipcClient.Send(msg)
}

// getIMAPCredentials returns IMAP credentials for an account.
func (v *ViewerApp) getIMAPCredentials(accountID string) (*imap.ClientConfig, error) {
	return v.composeOps.getIMAPCredentials(v.ctx, accountID)
}

// withIMAPRetry wraps an IMAP operation with stale-connection retry,
// mirroring App.withIMAPRetry.
func (v *ViewerApp) withIMAPRetry(accountID string, op func(conn *imap.Client) error) error {
	log := logging.WithComponent("viewer.imapRetry")

	poolConn, err := v.imapPool.GetConnection(v.ctx, accountID)
	if err != nil {
		return fmt.Errorf("failed to get IMAP connection: %w", err)
	}

	err = op(poolConn.Client())
	if err == nil {
		v.imapPool.Release(poolConn)
		return nil
	}

	if !imap.IsConnectionError(err) {
		v.imapPool.Release(poolConn)
		return err
	}

	log.Warn().Err(err).Str("account", accountID).Msg("IMAP connection error, retrying with fresh connection")
	v.imapPool.Discard(poolConn)

	poolConn, err = v.imapPool.GetConnection(v.ctx, accountID)
	if err != nil {
		return fmt.Errorf("failed to get IMAP connection on retry: %w", err)
	}
	defer v.imapPool.Release(poolConn)

	return op(poolConn.Client())
}

// ============================================================================
// Wails-bound methods (exposed to frontend)
// ============================================================================

// GetViewMode returns the thread this popup was opened for.
func (v *ViewerApp) GetViewMode() *ViewMode {
	folderType := ""
	if f, err := v.folderStore.Get(v.config.FolderID); err == nil && f != nil {
		folderType = string(f.Type)
	}
	return &ViewMode{
		AccountID:  v.config.AccountID,
		FolderID:   v.config.FolderID,
		FolderType: folderType,
		ThreadID:   v.config.ThreadID,
	}
}

// GetThemeMode returns the current theme mode setting.
func (v *ViewerApp) GetThemeMode() (string, error) {
	return v.settingsStore.GetThemeMode()
}

// GetSystemTheme returns the current system theme preference.
func (v *ViewerApp) GetSystemTheme() string {
	return platform.ReadSystemTheme()
}

// GetShowTitleBar returns whether the custom title bar should be shown.
func (v *ViewerApp) GetShowTitleBar() (bool, error) {
	return v.settingsStore.GetShowTitleBar()
}

// IsFlatpak returns true if the application is running inside a Flatpak sandbox.
func (v *ViewerApp) IsFlatpak() bool {
	return platform.IsFlatpak()
}

// NotifyStartupComplete signals the desktop environment that startup is done.
func (v *ViewerApp) NotifyStartupComplete() {
	platform.NotifyStartupComplete()
}

// RefreshWindowConstraints removes GTK max size constraints Wails sets at startup.
func (v *ViewerApp) RefreshWindowConstraints() {
	wailsRuntime.WindowSetMaxSize(v.ctx, 100000, 100000)
}

// BeforeClose is called when the window is about to close (e.g. the OS
// close button on the native title bar). The viewer holds no unsaved
// state, so it only vetoes until the frontend confirms the close.
func (v *ViewerApp) BeforeClose(ctx context.Context) bool {
	if v.closeConfirmed {
		return false
	}
	wailsRuntime.EventsEmit(v.ctx, "viewer:close-requested")
	return true
}

// ConfirmClose marks the close as user-confirmed so BeforeClose lets the
// programmatic close through.
func (v *ViewerApp) ConfirmClose() {
	v.closeConfirmed = true
}

// CloseWindow requests the window to close.
func (v *ViewerApp) CloseWindow() {
	wailsRuntime.Quit(v.ctx)
}

// SwitchView moves this thread to another view mode in the main window
// ("tab" or "dialog") and closes the popup. The main window opens the
// message; the popup does not wait for confirmation.
func (v *ViewerApp) SwitchView(mode string) error {
	if mode != "tab" && mode != "dialog" {
		return fmt.Errorf("unknown view mode: %s", mode)
	}
	v.notifySwitch(mode)
	v.closeConfirmed = true
	wailsRuntime.Quit(v.ctx)
	return nil
}

// OpenComposerForReply spawns a detached composer window for a reply,
// reply-all or forward of the given message. The composer is a separate
// process authenticated with the same IPC token this popup uses, so it
// talks to the same main window — the popup itself never composes.
func (v *ViewerApp) OpenComposerForReply(mode, messageID string) error {
	switch mode {
	case "reply", "reply-all", "forward":
	default:
		return fmt.Errorf("unknown compose mode: %s", mode)
	}
	return v.spawnComposer(v.config.AccountID, mode, messageID, "", "")
}

// OpenComposerForAddress spawns a detached composer addressed to one
// recipient (mailto: links inside the viewed message).
func (v *ViewerApp) OpenComposerForAddress(toAddress string) error {
	if strings.TrimSpace(toAddress) == "" {
		return fmt.Errorf("no address given")
	}
	return v.spawnComposer(v.config.AccountID, "new", "", "", "mailto:"+strings.TrimSpace(toAddress))
}

// OpenComposerForDraft spawns a detached composer resuming an existing draft.
func (v *ViewerApp) OpenComposerForDraft(draftID string) error {
	if strings.TrimSpace(draftID) == "" {
		return fmt.Errorf("no draft given")
	}
	return v.spawnComposer(v.config.AccountID, "new", "", draftID, "")
}

// spawnComposer starts a --compose child process with the given arguments,
// reusing this popup's IPC address and token.
func (v *ViewerApp) spawnComposer(accountID, mode, messageID, draftID, mailtoURL string) error {
	log := logging.WithComponent("viewer.ipc")

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	args := []string{
		"--compose",
		"--account", accountID,
		"--ipc-address", v.config.IPCAddress,
	}
	if draftID != "" {
		args = append(args, "--draft-id", draftID)
	} else if mode != "" && mode != "new" {
		args = append(args, "--mode", mode)
		if messageID != "" {
			args = append(args, "--message-id", messageID)
		}
	}
	if mailtoURL != "" {
		args = append(args, "--mailto", mailtoURL)
	}
	if lvl := logging.LevelName(); lvl != "" {
		args = append(args, "--log-level", lvl)
	}
	if f := logging.FilePath(); f != "" {
		args = append(args, "--log-file", composerLogPath(f))
	}

	log.Info().Strs("args", args).Msg("Spawning composer window from viewer popup")

	cmd := exec.Command(execPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start composer: %w", err)
	}
	go func() {
		defer recoverPanic("viewer.ipc", "write IPC token")
		_, _ = stdin.Write([]byte(v.ipcToken))
		stdin.Close()
	}()
	return nil
}

// ============================================================================
// Conversation loading and message actions
// ============================================================================

// GetConversation returns all messages in a conversation/thread.
func (v *ViewerApp) GetConversation(threadID, folderID string) (*message.Conversation, error) {
	return v.messageStore.GetConversation(threadID, folderID)
}

// GetSpecialFolder returns the account's special folder of the given type
// (trash, archive, ...). Exposed so the popup viewer can tell a move to
// Trash apart from other moves when deciding whether its conversation was
// deleted.
func (v *ViewerApp) GetSpecialFolder(accountID string, folderType folder.Type) (*folder.Folder, error) {
	return v.composeOps.getSpecialFolder(accountID, folderType)
}

// GetMessage returns a single message by internal ID.
func (v *ViewerApp) GetMessage(id string) (*message.Message, error) {
	return v.messageStore.Get(id)
}

// GetMessageSource fetches the raw RFC822 source of a message from IMAP.
func (v *ViewerApp) GetMessageSource(messageID string) (string, error) {
	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return "", fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return "", fmt.Errorf("message not found: %s", messageID)
	}
	rawBytes, truncated, err := v.syncEngine.FetchRawMessageWithTruncation(v.ctx, msg.AccountID, msg.FolderID, msg.UID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch message source: %w", err)
	}
	// R20: disclose a source cut at the 50 MB cap (mirrors
	// App.GetMessageSource) instead of showing a partial source as
	// if it were whole.
	source := string(rawBytes)
	if truncated {
		source += "\n\n[Daak: message source truncated — only the first 50 MB were fetched from the server.]"
	}
	return source, nil
}

// FetchMessageBody fetches the body for a message on-demand.
// Mirrors App.FetchMessageBody: a previously failed fetch (BodyFailed)
// is retried on open instead of returned early (re-audit finding R10).
func (v *ViewerApp) FetchMessageBody(messageID string) (*message.Message, error) {
	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}
	if msg.BodyFetched && !msg.BodyFailed {
		return msg, nil
	}
	return v.syncEngine.FetchMessageBody(v.ctx, msg.AccountID, messageID)
}

// expandToFolderCopies widens an action's target ids to every same-folder
// row sharing a Message-ID with them (mirrors App).
func (v *ViewerApp) expandToFolderCopies(messageIDs []string) []string {
	expanded, err := v.messageStore.ExpandToFolderCopies(messageIDs)
	if err != nil {
		log := logging.WithComponent("viewer")
		log.Warn().Err(err).Msg("Failed to expand action to duplicate copies")
		return messageIDs
	}
	return expanded
}

// MarkAsRead marks messages as read.
func (v *ViewerApp) MarkAsRead(messageIDs []string) error {
	return v.setReadStatus(messageIDs, true)
}

// MarkAsUnread marks messages as unread.
func (v *ViewerApp) MarkAsUnread(messageIDs []string) error {
	return v.setReadStatus(messageIDs, false)
}

func (v *ViewerApp) setReadStatus(messageIDs []string, isRead bool) error {
	log := logging.WithComponent("viewer")
	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)

	messages, err := v.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	isReadPtr := &isRead
	if err := v.messageStore.UpdateFlagsBatch(messageIDs, isReadPtr, nil); err != nil {
		return fmt.Errorf("failed to update local flags: %w", err)
	}
	if err := v.messageStore.RecordPendingFlagChanges(messages, message.FlagTypeRead, isRead); err != nil {
		log.Warn().Err(err).Msg("Local read flag updated but its pending row could not be recorded — a failed push may flip it back")
	}
	wailsRuntime.EventsEmit(v.ctx, "messages:readChanged", map[string]interface{}{
		"messageIds": messageIDs,
		"isRead":     isRead,
	})

	// Sync to IMAP in background with retry (same as App).
	go func() {
		defer recoverPanic("viewer.actions", "sync flags to IMAP")
		pushFlagChangesByFolder(v.messageStore, v.syncFlagsToIMAP, func(event string, data map[string]interface{}) {
			wailsRuntime.EventsEmit(v.ctx, event, data)
		}, byFolder, message.FlagTypeRead, isRead)
	}()
	return nil
}

// Star marks messages as starred.
func (v *ViewerApp) Star(messageIDs []string) error {
	return v.setStarredStatus(messageIDs, true)
}

// Unstar removes star from messages.
func (v *ViewerApp) Unstar(messageIDs []string) error {
	return v.setStarredStatus(messageIDs, false)
}

func (v *ViewerApp) setStarredStatus(messageIDs []string, isStarred bool) error {
	log := logging.WithComponent("viewer")
	if len(messageIDs) == 0 {
		return nil
	}
	// Star/unstar covers every same-folder copy sharing a Message-ID,
	// exactly like read/unread already does (fourth-audit finding 43).
	messageIDs = v.expandToFolderCopies(messageIDs)
	messages, err := v.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	isStarredPtr := &isStarred
	if err := v.messageStore.UpdateFlagsBatch(messageIDs, nil, isStarredPtr); err != nil {
		return fmt.Errorf("failed to update local flags: %w", err)
	}
	if err := v.messageStore.RecordPendingFlagChanges(messages, message.FlagTypeStarred, isStarred); err != nil {
		log.Warn().Err(err).Msg("Local star flag updated but its pending row could not be recorded — a failed push may flip it back")
	}
	wailsRuntime.EventsEmit(v.ctx, "messages:starredChanged", map[string]interface{}{
		"messageIds": messageIDs,
		"isStarred":  isStarred,
	})

	go func() {
		defer recoverPanic("viewer.actions", "sync star flags to IMAP")
		pushFlagChangesByFolder(v.messageStore, v.syncFlagsToIMAP, func(event string, data map[string]interface{}) {
			wailsRuntime.EventsEmit(v.ctx, event, data)
		}, byFolder, message.FlagTypeStarred, isStarred)
	}()
	return nil
}

// syncFlagsToIMAP syncs flag changes to the IMAP server.
func (v *ViewerApp) syncFlagsToIMAP(messages []*message.Message, folderID, flagType string, flagValue bool) error {
	if len(messages) == 0 {
		return nil
	}
	folderObj, err := v.folderStore.Get(folderID)
	if err != nil || folderObj == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}
	uids := make([]goImap.UID, len(messages))
	for i, m := range messages {
		uids[i] = goImap.UID(m.UID)
	}
	var flag goImap.Flag
	switch flagType {
	case "read":
		flag = goImap.FlagSeen
	case "starred":
		flag = goImap.FlagFlagged
	}
	return v.withIMAPRetry(messages[0].AccountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(v.ctx, folderObj.Path); err != nil {
			return fmt.Errorf("failed to select mailbox: %w", err)
		}
		if flagValue {
			return conn.AddMessageFlags(uids, []goImap.Flag{flag})
		}
		return conn.RemoveMessageFlags(uids, []goImap.Flag{flag})
	})
}

// moveMessagesToFolder moves messages locally first, then syncs the move to
// IMAP (COPY + DELETE) in the background. Unlike App.MoveToFolder it does
// not push an undo command — this process has no undo stack — but like it,
// it then syncs the destination folder and deletes the temp rows only once
// the real server rows are confirmed there (re-audit finding R15). Without
// that step a move from a popup left the temp rows behind, so the message
// appeared twice, and a second move sent a bogus UID MOVE for the
// placeholder.
func (v *ViewerApp) moveMessagesToFolder(messageIDs []string, destFolderID string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)

	messages, err := v.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}
	destFolder, err := v.folderStore.Get(destFolderID)
	if err != nil || destFolder == nil {
		return fmt.Errorf("destination folder not found: %s", destFolderID)
	}
	// The popup shows a single thread, so a cross-account move cannot
	// arise here; refuse rather than silently moving half a batch.
	accountID := messages[0].AccountID
	for _, m := range messages {
		if m.AccountID != accountID {
			return fmt.Errorf("cross-account move is not supported from a viewer popup")
		}
	}
	if destFolder.AccountID != "" && destFolder.AccountID != accountID {
		return fmt.Errorf("destination folder belongs to another account")
	}

	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	if err := v.messageStore.MoveMessages(messageIDs, destFolderID); err != nil {
		return fmt.Errorf("failed to move messages locally: %w", err)
	}
	wailsRuntime.EventsEmit(v.ctx, "messages:moved", map[string]interface{}{
		"messageIds":   messageIDs,
		"destFolderId": destFolderID,
	})

	go func() {
		defer recoverPanic("viewer.actions", "move messages on IMAP")
		log := logging.WithComponent("viewer")
		var movedOK []*message.Message
		for sourceFolderID, msgs := range byFolder {
			if err := v.moveMessagesOnIMAP(msgs, sourceFolderID, destFolder); err != nil {
				log.Error().Err(err).Str("sourceFolderID", sourceFolderID).Str("destFolderID", destFolderID).Msg("Failed to move messages on IMAP")
				continue
			}
			movedOK = append(movedOK, msgs...)
		}
		if len(movedOK) == 0 {
			return
		}
		// Sync the destination folder so the moved messages get their
		// real server UIDs, then delete the temp rows only for the
		// messages whose server copy the sync confirmed (re-audit
		// finding R15). A failed sync, or an unconfirmed copy, keeps
		// the temp rows — and with them the message's local state.
		// Sources whose IMAP move failed are not cleaned up at all:
		// their server copy was never made.
		if _, err := cleanupMovedTempUIDs(v.messageStore, destFolderID, movedOK, func() error {
			return v.syncDestinationFolder(accountID, destFolderID)
		}); err != nil {
			log.Warn().Err(err).Str("destFolderID", destFolderID).Msg("Keeping temp UIDs after move: destination not reconciled")
		}
	}()
	return nil
}

// syncDestinationFolder runs the header sync for the destination folder —
// the viewer's minimal equivalent of App.SyncFolder (re-audit finding
// R15). The viewer process has no debounce/cancel registry, sender
// rules, or background body fetch; those belong to the main window, which
// runs its own sync pass anyway. The header sync is what fetches the
// moved messages' real server UIDs, which is all the post-move cleanup
// needs. It reuses the viewer's sync engine (constructed in Startup,
// never started) and the same SyncMessages call App.SyncFolder makes.
func (v *ViewerApp) syncDestinationFolder(accountID, destFolderID string) error {
	syncPeriodDays := 30 // default (0 means all messages), as in App.SyncFolder
	if acc, err := v.accountStore.Get(accountID); err == nil && acc != nil {
		syncPeriodDays = acc.SyncPeriodDays
	}
	return v.syncEngine.SyncMessages(v.ctx, accountID, destFolderID, syncPeriodDays, false)
}

// isGmailAccount reports whether the account uses Gmail's IMAP server.
// Gmail uses labels instead of folders — an IMAP EXPUNGE in one mailbox
// destroys the one underlying message across ALL labels.
func (v *ViewerApp) isGmailAccount(accountID string) bool {
	acc, err := v.accountStore.Get(accountID)
	if err != nil || acc == nil {
		return false
	}
	return acc.IMAPHost == "imap.gmail.com"
}

// deleteMessagesFromIMAP mirrors App.deleteMessagesFromIMAP: placeholder
// UIDs (0 or negative — local-only rows with no server counterpart) are
// never sent to the server, and on Gmail a message that still has copies
// in other local folders is left alone — expunging it here would destroy
// it in every folder.
func (v *ViewerApp) deleteMessagesFromIMAP(messages []*message.Message, folderID string) error {
	if len(messages) == 0 {
		return nil
	}
	log := logging.WithComponent("viewer.deleteMessagesFromIMAP")
	folderObj, err := v.folderStore.Get(folderID)
	if err != nil || folderObj == nil {
		return fmt.Errorf("folder not found")
	}
	accountID := messages[0].AccountID
	isGmail := v.isGmailAccount(accountID)
	var uids []goImap.UID
	for _, m := range messages {
		if m.UID == 0 || int32(m.UID) < 0 {
			continue
		}
		if isGmail && m.MessageID != "" {
			hasCopies, copyErr := v.messageStore.HasCopiesInOtherFolders(m.MessageID, folderID, accountID)
			if copyErr != nil {
				log.Warn().Err(copyErr).Str("messageID", m.MessageID).Msg("Failed to check for copies in other folders")
			}
			if hasCopies {
				log.Debug().Str("messageID", m.MessageID).Msg("Gmail: skipping IMAP delete — message exists in other folders")
				continue
			}
		}
		uids = append(uids, goImap.UID(m.UID))
	}
	if len(uids) == 0 {
		return nil
	}
	return v.withIMAPRetry(accountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(v.ctx, folderObj.Path); err != nil {
			return fmt.Errorf("failed to select mailbox: %w", err)
		}
		return conn.DeleteMessagesByUID(uids)
	})
}

// moveMessagesOnIMAP copies messages to the destination mailbox and deletes
// the originals. Gmail note: the main window partitions copies for
// Trash/Spam so a message with copies elsewhere only loses the label;
// the popup mirrors that partition — a copy elsewhere gets DELETE only
// (no COPY), so the Trash/Spam label is never added to the one
// underlying Gmail message and it stays visible in its other folders.
// Placeholder UIDs have no server counterpart and are skipped.
func (v *ViewerApp) moveMessagesOnIMAP(messages []*message.Message, sourceFolderID string, destFolder *folder.Folder) error {
	if len(messages) == 0 {
		return nil
	}
	sourceFolder, err := v.folderStore.Get(sourceFolderID)
	if err != nil || sourceFolder == nil {
		return fmt.Errorf("source folder not found")
	}
	log := logging.WithComponent("viewer.moveMessagesOnIMAP")
	accountID := messages[0].AccountID
	isGmail := v.isGmailAccount(accountID)
	destIsTrashOrSpam := destFolder.Type == folder.TypeTrash || destFolder.Type == folder.TypeSpam
	var moveUIDs, labelRemovalUIDs []goImap.UID
	for _, m := range messages {
		if m.UID == 0 || int32(m.UID) < 0 {
			continue
		}
		if isGmail && destIsTrashOrSpam && m.MessageID != "" {
			hasCopies, copyErr := v.messageStore.HasCopiesInOtherFolders(m.MessageID, sourceFolderID, accountID)
			if copyErr != nil {
				log.Warn().Err(copyErr).Str("messageID", m.MessageID).Msg("Failed to check for copies, treating as sole copy")
			}
			if hasCopies {
				labelRemovalUIDs = append(labelRemovalUIDs, goImap.UID(m.UID))
				continue
			}
		}
		moveUIDs = append(moveUIDs, goImap.UID(m.UID))
	}
	allUIDs := append(moveUIDs, labelRemovalUIDs...)
	if len(allUIDs) == 0 {
		return nil
	}
	return v.withIMAPRetry(accountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(v.ctx, sourceFolder.Path); err != nil {
			return fmt.Errorf("failed to select mailbox: %w", err)
		}
		if len(moveUIDs) > 0 {
			if _, err := conn.CopyMessages(moveUIDs, destFolder.Path); err != nil {
				return fmt.Errorf("failed to copy messages: %w", err)
			}
		}
		return conn.DeleteMessagesByUID(allUIDs)
	})
}

// Archive moves messages to the Archive folder.
func (v *ViewerApp) Archive(messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)
	messages, err := v.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return fmt.Errorf("failed to get message")
	}
	archiveFolder, err := v.composeOps.getSpecialFolder(messages[0].AccountID, folder.TypeArchive)
	if err != nil {
		return fmt.Errorf("failed to get archive folder: %w", err)
	}
	if archiveFolder == nil {
		return fmt.Errorf("no archive folder configured")
	}
	return v.moveMessagesToFolder(messageIDs, archiveFolder.ID)
}

// Trash moves messages to the Trash folder. Returns true when the move was
// applied (mirrors App.Trash's signature).
func (v *ViewerApp) Trash(messageIDs []string) (bool, error) {
	if len(messageIDs) == 0 {
		return false, nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)
	messages, err := v.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return false, fmt.Errorf("failed to get message")
	}
	trashFolder, err := v.composeOps.getSpecialFolder(messages[0].AccountID, folder.TypeTrash)
	if err != nil {
		return false, fmt.Errorf("failed to get trash folder: %w", err)
	}
	if trashFolder == nil {
		return false, fmt.Errorf("no trash folder configured")
	}
	return true, v.moveMessagesToFolder(messageIDs, trashFolder.ID)
}

// MarkAsSpam moves messages to the Spam folder.
func (v *ViewerApp) MarkAsSpam(messageIDs []string) (bool, error) {
	if len(messageIDs) == 0 {
		return false, nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)
	messages, err := v.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return false, fmt.Errorf("failed to get message")
	}
	spamFolder, err := v.composeOps.getSpecialFolder(messages[0].AccountID, folder.TypeSpam)
	if err != nil {
		return false, fmt.Errorf("failed to get spam folder: %w", err)
	}
	if spamFolder == nil {
		return false, fmt.Errorf("no spam folder configured")
	}
	return true, v.moveMessagesToFolder(messageIDs, spamFolder.ID)
}

// MarkAsNotSpam moves messages from Spam back to the Inbox.
func (v *ViewerApp) MarkAsNotSpam(messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)
	messages, err := v.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return fmt.Errorf("failed to get message")
	}
	inboxFolder, err := v.composeOps.getSpecialFolder(messages[0].AccountID, folder.TypeInbox)
	if err != nil {
		return fmt.Errorf("failed to get inbox folder: %w", err)
	}
	if inboxFolder == nil {
		return fmt.Errorf("no inbox folder configured")
	}
	return v.moveMessagesToFolder(messageIDs, inboxFolder.ID)
}

// DeletePermanently deletes messages locally and from the server.
// There is no confirmation inside the popup: ConversationViewer only
// calls this after its own delete confirmation.
func (v *ViewerApp) DeletePermanently(messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = v.expandToFolderCopies(messageIDs)
	messages, err := v.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}
	if err := v.messageStore.DeleteBatch(messageIDs); err != nil {
		return fmt.Errorf("failed to delete messages locally: %w", err)
	}
	wailsRuntime.EventsEmit(v.ctx, "messages:deleted", messageIDs)

	go func() {
		defer recoverPanic("viewer.actions", "delete messages on IMAP")
		for folderID, msgs := range byFolder {
			if err := v.deleteMessagesFromIMAP(msgs, folderID); err != nil {
				log := logging.WithComponent("viewer")
				log.Error().Err(err).Str("folderID", folderID).Msg("Failed to delete messages from IMAP")
			}
		}
	}()
	return nil
}

// Undo is not supported in a viewer popup: this process holds no undo
// stack, and actions taken here never reach the main window's stack.
// The frontend surfaces the error as a toast.
func (v *ViewerApp) Undo() (string, error) {
	return "", fmt.Errorf("nothing to undo in a popup window")
}

// GetCodeChipClickMode returns the code-chip click mode.
func (v *ViewerApp) GetCodeChipClickMode() (string, error) {
	return v.settingsStore.GetCodeChipClickMode()
}

// GetOtpClipboardClearSeconds returns the clipboard-clear delay in seconds.
func (v *ViewerApp) GetOtpClipboardClearSeconds() (int, error) {
	return v.settingsStore.GetOtpClipboardClearSeconds()
}

// ApplyCodeChipAction performs the backend half of a code-chip click in a
// viewer popup (mirrors App.ApplyCodeChipAction). Same modes; the only
// difference is Undo: a popup holds no undo stack, so a move to Trash
// cannot be undone from here — the frontend omits the Undo action in
// popup mode.
func (v *ViewerApp) ApplyCodeChipAction(messageID string, mode string) (bool, error) {
	if mode == "" {
		var err error
		mode, err = v.settingsStore.GetCodeChipClickMode()
		if err != nil {
			return false, err
		}
	}
	switch mode {
	case settings.CodeChipClickCopyMarkReadDelete,
		settings.CodeChipClickCopyMarkRead,
		settings.CodeChipClickCopyOnly:
	default:
		return false, fmt.Errorf("invalid code chip click mode: %s", mode)
	}

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return false, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return false, fmt.Errorf("message not found: %s", messageID)
	}
	if msg.ExtractKind == "" {
		return false, fmt.Errorf("message has no extracted value")
	}

	moved := false
	if mode != settings.CodeChipClickCopyOnly && !msg.IsRead {
		if err := v.MarkAsRead([]string{messageID}); err != nil {
			return false, fmt.Errorf("failed to mark message read: %w", err)
		}
	}
	if mode == settings.CodeChipClickCopyMarkReadDelete {
		moved, err = v.Trash([]string{messageID})
		if err != nil {
			return false, fmt.Errorf("failed to move message to Trash: %w", err)
		}
	}

	wailsRuntime.EventsEmit(v.ctx, "attention:changed", messageID)
	return moved, nil
}

// ============================================================================
// Reactions, read receipts, images, links
// ============================================================================

// GetMessageReactions returns the current reactions on a message.
func (v *ViewerApp) GetMessageReactions(messageID string) ([]MessageReaction, error) {
	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}
	reactions, err := v.reactionStore.ListByMessage(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to list reactions: %w", err)
	}
	ownEmails := v.ownEmailsForAccount(msg.AccountID)
	result := make([]MessageReaction, 0, len(reactions))
	for _, r := range reactions {
		result = append(result, MessageReaction{
			Emoji:        r.Emoji,
			ReactorEmail: r.ReactorEmail,
			ReactorName:  r.ReactorName,
			CreatedAt:    r.CreatedAt,
			IsOwn:        ownEmails[strings.ToLower(strings.TrimSpace(r.ReactorEmail))],
		})
	}
	return result, nil
}

// RemoveOwnReaction deletes only the current user's own reaction on a
// message from the local store. It sends no email (mirrors App).
func (v *ViewerApp) RemoveOwnReaction(messageID string) error {
	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}
	own := v.ownEmailsForAccount(msg.AccountID)
	emails := make([]string, 0, len(own))
	for email := range own {
		emails = append(emails, email)
	}
	if _, err := v.reactionStore.RemoveReaction(messageID, emails); err != nil {
		return fmt.Errorf("failed to remove reaction: %w", err)
	}
	// A reaction still queued (inside the undo window) is cancelled
	// as well, so it does not go out anyway and reappear on the next
	// Sent sync. A dispatched reaction stays local-only.
	if v.sendQueue != nil {
		if n, err := v.sendQueue.store.DeletePendingReaction(messageID); err == nil && n > 0 {
			v.sendQueue.emitEvent("sendqueue:updated", nil)
		}
	}
	return nil
}

// SendReaction queues an emoji reaction as a reaction email to the
// original sender only, for background send after the undo window
// (mirrors App.sendReaction).
func (v *ViewerApp) SendReaction(messageID string, emoji string) error {
	return v.sendReaction(v.ctx, messageID, emoji)
}

// ============================================================================
// Unsubscribe (Phase C) - mirrors App; the shared work lives on
// unsubscribeDeps, so the popup window behaves exactly like the main one.
// ============================================================================

// GetUnsubscribeStatuses returns the unsubscribe status for each
// message id.
func (v *ViewerApp) GetUnsubscribeStatuses(messageIDs []string) ([]*UnsubscribeStatus, error) {
	return v.unsubscribeDeps().getStatuses(messageIDs), nil
}

// UnsubscribeFromMessage carries out the unsubscribe for one message
// (internal id).
func (v *ViewerApp) UnsubscribeFromMessage(messageID string) (*UnsubscribeResult, error) {
	return v.unsubscribeDeps().unsubscribeFromMessage(v.ctx, messageID)
}

// UnsubscribeFollowUp applies the optional extras after an unsubscribe:
// move the sender's existing inbox mail to Trash, and/or auto-delete
// future mail from the sender.
func (v *ViewerApp) UnsubscribeFollowUp(messageID string, moveExisting bool, autoDelete bool) error {
	return v.unsubscribeDeps().followUp(messageID, moveExisting, autoDelete)
}

// GetSubscriptions returns every sender who ever sent List-Unsubscribe
// headers, most messages first, for Settings → Subscriptions.
func (v *ViewerApp) GetSubscriptions() ([]SubscriptionSender, error) {
	return v.unsubscribeDeps().listSubscriptions()
}

// UnsubscribeFromSubscription unsubscribes from a sender listed in the
// Subscriptions view.
func (v *ViewerApp) UnsubscribeFromSubscription(senderEmail string, accountID string) (*UnsubscribeResult, error) {
	return v.unsubscribeDeps().unsubscribeFromSubscription(v.ctx, senderEmail, accountID)
}

// SetSenderAutoDelete creates or removes the auto-delete rule for a
// sender: future mail from the sender goes to Trash at sync time.
func (v *ViewerApp) SetSenderAutoDelete(senderEmail string, accountID string, enabled bool) error {
	return v.unsubscribeDeps().setSenderAutoDelete(senderEmail, accountID, enabled)
}

func (v *ViewerApp) unsubscribeDeps() *unsubscribeDeps {
	return &unsubscribeDeps{
		messageStore: v.messageStore,
		accountStore: v.accountStore,
		folderStore:  v.folderStore,
		composeOps:   &v.composeOps,
		trash:        v.Trash,
	}
}

func (v *ViewerApp) sendReaction(ctx context.Context, messageID, emoji string) error {
	log := logging.WithComponent("viewer")

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}
	if strings.TrimSpace(msg.FromEmail) == "" {
		return fmt.Errorf("original message has no sender to react to")
	}

	from := smtp.Address{}
	identities, _ := v.accountStore.GetIdentities(msg.AccountID)
	if ident := selectReplyFromIdentity(identities, msg); ident != nil {
		from = smtp.Address{Name: ident.Name, Address: ident.Email}
	} else if acc, accErr := v.accountStore.Get(msg.AccountID); accErr == nil && acc != nil {
		from = smtp.Address{Name: acc.Name, Address: acc.Email}
	}
	if from.Address == "" {
		return fmt.Errorf("no sender identity for account %s", msg.AccountID)
	}

	orig := reaction.Original{
		MessageID:      msg.MessageID,
		Subject:        msg.Subject,
		FromName:       msg.FromName,
		FromEmail:      msg.FromEmail,
		To:             addressStrings(parseAddressList(msg.ToList)),
		Cc:             addressStrings(parseAddressList(msg.CcList)),
		Bcc:            addressStrings(parseAddressList(msg.BccList)),
		Date:           msg.Date,
		SMIMEEncrypted: msg.SMIMEEncrypted,
		PGPEncrypted:   msg.PGPEncrypted,
	}
	if err := reaction.ValidateReaction(emoji, orig, v.ownEmailsForAccount(msg.AccountID)); err != nil {
		return err
	}

	// The quoted original needs the full body: fetch it first when it
	// has not been fetched yet, rather than sending a body-less quote.
	// Limits run first, so a disallowed reaction fails fast without
	// a fetch.
	if strings.TrimSpace(msg.BodyText) == "" && strings.TrimSpace(msg.BodyHTML) == "" {
		fetched, err := v.FetchMessageBody(messageID)
		if err != nil {
			return fmt.Errorf("failed to fetch original body for the reaction quote: %w", err)
		}
		msg = fetched
	}

	raw, reactionMessageID, err := reaction.BuildMessage(reaction.BuildOptions{
		Emoji:            emoji,
		SenderName:       from.Name,
		SenderEmail:      from.Address,
		Original:         orig,
		References:       threadReferences(msg),
		OriginalBodyText: msg.BodyText,
		OriginalBodyHTML: msg.BodyHTML,
	})
	if err != nil {
		return fmt.Errorf("failed to build reaction message: %w", err)
	}

	// Queue through the shared Outbox (mirrors App.sendReaction):
	// the main process's worker sends at dispatch. This process
	// never dispatches and never shows the Undo toast itself — the
	// main window owns both, so notify it over IPC.
	to := smtp.Address{Name: msg.FromName, Address: strings.TrimSpace(msg.FromEmail)}
	res, err := v.sendQueue.QueueReaction(msg.AccountID, from, []string{to.Address}, raw,
		reaction.ReactionSubject(orig.Subject), to.Address, msg.ID)
	if err != nil {
		return fmt.Errorf("failed to queue reaction: %w", err)
	}

	rec := &reaction.Reaction{
		MessageID:       msg.ID,
		TargetMessageID: msg.MessageID,
		ReactorEmail:    from.Address,
		ReactorName:     from.Name,
		Emoji:           emoji,
		CreatedAt:       time.Now().UTC(),
		SourceMessageID: reactionMessageID,
	}
	if err := v.reactionStore.Record(rec); err != nil {
		_, _ = v.sendQueue.store.Delete(res.QueueID)
		return fmt.Errorf("reaction queued but failed to record: %w", err)
	}

	// Notify last, after the reaction is queued AND recorded: a
	// failed notification must not lose the pill or invite a retry
	// that would queue a second reaction email. The reaction is
	// queued and the pill shows either way — only the countdown
	// toast is missing — so surface the failure as an error and let
	// the frontend say Undo lives in the Outbox.
	if err := v.notifyReactionQueued(msg.AccountID, reaction.ReactionSubject(orig.Subject), to.Address, res); err != nil {
		log.Warn().Err(err).Str("queueID", res.QueueID).
			Msg("Reaction queued and recorded, but the main window could not be notified — no countdown toast will appear")
		return fmt.Errorf("reaction queued, but the main window could not be notified: %w", err)
	}

	log.Info().Str("messageID", messageID).Str("emoji", emoji).Str("from", from.Address).Str("to", to.Address).Str("queueID", res.QueueID).Msg("Reaction queued")
	return nil
}

// notifyReactionQueued tells the main window a reaction was queued,
// so it can show the countdown toast with Undo. Mirrors the
// detached composer's notifyMessageQueued: a failed notification is
// an error to surface, never a silent drop.
func (v *ViewerApp) notifyReactionQueued(accountID, subject, recipient string, res QueueSendResult) error {
	if v.ipcClient == nil {
		return fmt.Errorf("no IPC connection to the main window")
	}
	ipcMsg, err := ipc.NewMessage(ipc.TypeMessageQueued, ipc.MessageQueuedPayload{
		QueueID:      res.QueueID,
		AccountID:    accountID,
		DraftID:      "",
		Subject:      subject,
		Recipient:    recipient,
		DelaySeconds: res.DelaySeconds,
	})
	if err != nil {
		return fmt.Errorf("failed to build message_queued notification: %w", err)
	}
	if err := v.ipcClient.Send(ipcMsg); err != nil {
		return fmt.Errorf("failed to send message_queued notification: %w", err)
	}
	return nil
}

// ownEmailsForAccount returns the user's own addresses for an account
// (mirrors App).
func (v *ViewerApp) ownEmailsForAccount(accountID string) map[string]bool {
	own := make(map[string]bool)
	if acc, err := v.accountStore.Get(accountID); err == nil && acc != nil && acc.Email != "" {
		own[strings.ToLower(strings.TrimSpace(acc.Email))] = true
	}
	if identities, err := v.accountStore.GetIdentities(accountID); err == nil {
		for _, ident := range identities {
			if ident.Email != "" {
				own[strings.ToLower(strings.TrimSpace(ident.Email))] = true
			}
		}
	}
	return own
}

// GetReadReceiptResponsePolicy returns the read-receipt response policy.
func (v *ViewerApp) GetReadReceiptResponsePolicy() (string, error) {
	return v.settingsStore.GetReadReceiptResponsePolicy()
}

// GetMarkAsReadDelay returns the delay before a viewed message is marked read.
func (v *ViewerApp) GetMarkAsReadDelay() (int, error) {
	return v.settingsStore.GetMarkAsReadDelay()
}

// SendReadReceipt sends a read receipt (MDN) for the specified message
// (mirrors App.SendReadReceipt).
func (v *ViewerApp) SendReadReceipt(accountID, messageID string) error {
	log := logging.WithComponent("viewer")

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}
	if msg.ReadReceiptTo == "" {
		return fmt.Errorf("message does not request a read receipt")
	}
	if msg.ReadReceiptHandled {
		return fmt.Errorf("read receipt already handled for this message")
	}

	acc, err := v.accountStore.Get(accountID)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}

	identities, err := v.accountStore.GetIdentities(accountID)
	if err != nil {
		return fmt.Errorf("failed to get identities: %w", err)
	}
	var fromName, fromEmail string
	for _, id := range identities {
		if id.IsDefault {
			fromName = id.Name
			fromEmail = id.Email
			break
		}
	}
	if fromEmail == "" && len(identities) > 0 {
		fromName = identities[0].Name
		fromEmail = identities[0].Email
	}
	if fromEmail == "" {
		fromEmail = acc.Email
		fromName = acc.Name
	}

	mdnBytes, err := smtp.BuildMDN(msg, fromName, fromEmail, smtp.MDNDisplayed)
	if err != nil {
		return fmt.Errorf("failed to build MDN: %w", err)
	}

	smtpConfig, err := v.composeOps.smtpClientConfig(v.ctx, acc)
	if err != nil {
		return err
	}

	// smtpClientConfig already resolves password/OAuth credentials, so
	// send through the normal client path.
	client := smtp.NewClient(smtpConfig)
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to SMTP: %w", err)
	}
	defer client.Close()
	if err := client.Login(); err != nil {
		return fmt.Errorf("failed to authenticate to SMTP: %w", err)
	}
	recipientEmail := extractEmailFromHeader(msg.ReadReceiptTo)
	if err := client.SendMail(fromEmail, []string{recipientEmail}, mdnBytes); err != nil {
		log.Error().Err(err).Str("from", fromEmail).Str("to", recipientEmail).Msg("Failed to send read receipt MDN")
		return fmt.Errorf("failed to send read receipt: %w", err)
	}
	if err := v.messageStore.MarkReadReceiptHandled(messageID); err != nil {
		log.Warn().Err(err).Str("message_id", messageID).Msg("Failed to mark read receipt as handled")
	}
	log.Info().Str("message_id", messageID).Str("to", recipientEmail).Msg("Read receipt sent")
	return nil
}

// IgnoreReadReceipt marks a message's read receipt request as ignored.
func (v *ViewerApp) IgnoreReadReceipt(accountID, messageID string) error {
	if err := v.messageStore.MarkReadReceiptHandled(messageID); err != nil {
		return fmt.Errorf("failed to mark read receipt as handled: %w", err)
	}
	return nil
}

// AddImageAllowlist adds a sender/domain to the remote-image allowlist.
func (v *ViewerApp) AddImageAllowlist(entryType, value string) error {
	return v.imageAllowlistStore.Add(entryType, value)
}

// OpenURL opens a URL in the system browser (mirrors App.OpenURL).
func (v *ViewerApp) OpenURL(url string) error {
	log := logging.WithComponent("viewer")
	if url == "" {
		return fmt.Errorf("empty URL")
	}
	if !isAllowedProtocol(url) {
		log.Warn().Str("url", url).Msg("Rejecting URL with disallowed protocol")
		return fmt.Errorf("URL protocol not allowed for security reasons")
	}

	// On Linux, try the OpenURI portal first (mirrors App.OpenURL).
	if runtime.GOOS == "linux" {
		if perr := platform.PortalOpenURI(url); perr == nil {
			return nil
		} else {
			log.Debug().Err(perr).Msg("Portal OpenURI failed, falling back to xdg-open")
		}
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		return platform.OpenURLWindows(url)
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Str("url", url).Msg("Failed to open URL in browser")
		return fmt.Errorf("failed to open URL: %w", err)
	}
	return nil
}

// ============================================================================
// S/MIME and PGP on-view processing (mirrors App)
// ============================================================================

// ProcessSMIMEMessage decrypts and/or verifies an S/MIME message on-view.
func (v *ViewerApp) ProcessSMIMEMessage(messageID string) (*SMIMEViewResult, error) {
	log := logging.WithComponent("viewer")

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}

	recipientEmail := v.findRecipientIdentityEmail(msg)

	rawBody, err := v.messageStore.GetSMIMERawBody(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get S/MIME raw body: %w", err)
	}
	if rawBody == nil {
		return nil, fmt.Errorf("no S/MIME raw body for message: %s", messageID)
	}

	result := &SMIMEViewResult{}
	innerBytes := rawBody

	if msg.SMIMEEncrypted {
		decrypted, isEncrypted, decErr := v.smimeDecryptor.DecryptMessage(msg.AccountID, recipientEmail, rawBody)
		if decErr != nil {
			log.Warn().Err(decErr).Str("messageID", messageID).Msg("S/MIME decryption failed")
			return &SMIMEViewResult{
				SMIMEEncrypted: true,
				SMIMEStatus:    "decrypt_failed",
			}, nil
		}
		if isEncrypted {
			result.SMIMEEncrypted = true
			innerBytes = decrypted
		}
	}

	var sigResult *smime.SignatureResult
	ct := extractContentType(innerBytes)
	if smime.IsSMIMESigned(ct) {
		sigResult, innerBytes = v.smimeVerifier.VerifyAndUnwrap(innerBytes)
		if innerBytes == nil {
			innerBytes = rawBody
		}
	}

	if sigResult != nil {
		result.SMIMEStatus = string(sigResult.Status)
		result.SMIMESignerEmail = sigResult.SignerEmail
		result.SMIMESignerSubject = sigResult.SignerName
	}

	parsed := v.syncEngine.ParseDecryptedBody(innerBytes, messageID)
	result.BodyHTML = parsed.BodyHTML
	result.BodyText = parsed.BodyText
	result.InlineAttachments = buildInlineAttachmentMap(parsed.Attachments)
	result.Attachments = buildDecryptedAttachmentList(parsed.Attachments)

	return result, nil
}

// ProcessPGPMessage decrypts and/or verifies a PGP message on-view.
func (v *ViewerApp) ProcessPGPMessage(messageID string) (*PGPViewResult, error) {
	log := logging.WithComponent("viewer")

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}

	recipientEmail := v.findRecipientIdentityEmail(msg)

	rawBody, err := v.messageStore.GetPGPRawBody(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get PGP raw body: %w", err)
	}
	if rawBody == nil {
		return nil, fmt.Errorf("no PGP raw body for message: %s", messageID)
	}

	result := &PGPViewResult{}
	innerBytes := rawBody

	if msg.PGPEncrypted {
		decrypted, isEncrypted, decErr := v.pgpDecryptor.DecryptMessage(msg.AccountID, recipientEmail, rawBody)
		if decErr != nil {
			log.Warn().Err(decErr).Str("messageID", messageID).Msg("PGP decryption failed")
			return &PGPViewResult{
				PGPEncrypted: true,
				PGPStatus:    "decrypt_failed",
			}, nil
		}
		if isEncrypted {
			result.PGPEncrypted = true
			innerBytes = decrypted
		}
	}

	var sigResult *pgp.SignatureResult
	ct := extractContentType(innerBytes)
	if pgp.IsPGPSigned(ct) {
		sigResult, innerBytes = v.pgpVerifier.VerifyAndUnwrap(innerBytes)
		if innerBytes == nil {
			innerBytes = rawBody
		}
	}

	if sigResult != nil {
		result.PGPStatus = string(sigResult.Status)
		result.PGPSignerEmail = sigResult.SignerEmail
		result.PGPSignerKeyID = sigResult.SignerKeyID
	}

	parsed := v.syncEngine.ParseDecryptedBody(innerBytes, messageID)
	result.BodyHTML = parsed.BodyHTML
	result.BodyText = parsed.BodyText
	result.InlineAttachments = buildInlineAttachmentMap(parsed.Attachments)
	result.Attachments = buildDecryptedAttachmentList(parsed.Attachments)

	return result, nil
}

// findRecipientIdentityEmail returns the identity email that was a recipient
// of the message (mirrors App).
func (v *ViewerApp) findRecipientIdentityEmail(msg *message.Message) string {
	identities, err := v.accountStore.GetIdentities(msg.AccountID)
	if err != nil || len(identities) == 0 {
		return ""
	}
	identityEmails := make(map[string]string)
	for _, id := range identities {
		identityEmails[strings.ToLower(strings.TrimSpace(id.Email))] = id.Email
	}
	for _, list := range []string{msg.ToList, msg.CcList, msg.BccList} {
		addrs := parseAddressList(list)
		for _, addr := range addrs {
			if email, ok := identityEmails[strings.ToLower(strings.TrimSpace(addr.Address))]; ok {
				return email
			}
		}
	}
	acc, err := v.accountStore.Get(msg.AccountID)
	if err == nil && acc != nil {
		return acc.Email
	}
	return ""
}

// ============================================================================
// Attachments (mirrors App)
// ============================================================================

// GetAttachments returns attachment metadata for a message.
func (v *ViewerApp) GetAttachments(messageID string) ([]*message.Attachment, error) {
	return v.attachmentStore.GetByMessage(messageID)
}

// GetAttachment returns a single attachment by ID.
func (v *ViewerApp) GetAttachment(attachmentID string) (*message.Attachment, error) {
	return v.attachmentStore.Get(attachmentID)
}

// GetInlineAttachments returns a map of content-id to data URL for all
// inline attachments of a message.
func (v *ViewerApp) GetInlineAttachments(messageID string) (map[string]string, error) {
	return v.attachmentStore.GetInlineByMessage(messageID)
}

// DownloadAttachment downloads an attachment and saves it to disk.
func (v *ViewerApp) DownloadAttachment(attachmentID, savePath string) (string, error) {
	log := logging.WithComponent("viewer")

	att, err := v.attachmentStore.Get(attachmentID)
	if err != nil {
		return "", fmt.Errorf("failed to get attachment: %w", err)
	}
	if att == nil {
		return "", fmt.Errorf("attachment not found: %s", attachmentID)
	}

	if savePath == "" && att.LocalPath != "" {
		if _, err := os.Stat(att.LocalPath); err == nil {
			return att.LocalPath, nil
		}
	}

	msg, err := v.messageStore.Get(att.MessageID)
	if err != nil {
		return "", fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return "", fmt.Errorf("message not found: %s", att.MessageID)
	}

	raw, err := v.syncEngine.FetchRawMessage(v.ctx, msg.AccountID, msg.FolderID, msg.UID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch message: %w", err)
	}

	downloader := email.NewAttachmentDownloader(v.paths.AttachmentsPath())
	content, err := downloader.ExtractAttachmentContentFor(raw, att)
	if err != nil {
		return "", fmt.Errorf("failed to extract attachment: %w", err)
	}

	localPath, err := downloader.SaveAttachment(att, content, savePath)
	if err != nil {
		return "", fmt.Errorf("failed to save attachment: %w", err)
	}

	if savePath == "" {
		if err := v.attachmentStore.UpdateLocalPath(attachmentID, localPath); err != nil {
			log.Warn().Err(err).Msg("Failed to update attachment local path")
		}
	}

	log.Info().Str("attachment", att.Filename).Str("path", localPath).Int("size", len(content)).Msg("Attachment downloaded")
	return localPath, nil
}

// OpenAttachment downloads (if needed) and opens an attachment.
func (v *ViewerApp) OpenAttachment(attachmentID string) error {
	localPath, err := v.DownloadAttachment(attachmentID, "")
	if err != nil {
		return err
	}
	return v.openFile(localPath)
}

// SaveAttachmentAs shows a Save As dialog and saves the attachment.
func (v *ViewerApp) SaveAttachmentAs(attachmentID string) (string, error) {
	log := logging.WithComponent("viewer")

	att, err := v.attachmentStore.Get(attachmentID)
	if err != nil {
		return "", fmt.Errorf("failed to get attachment: %w", err)
	}
	if att == nil {
		return "", fmt.Errorf("attachment not found: %s", attachmentID)
	}

	defaultDir := platform.DefaultSaveDir()

	if platform.IsFlatpak() {
		savePath, err := platform.PortalSaveFile("Save Attachment", att.Filename, defaultDir)
		if err != nil {
			return "", fmt.Errorf("failed to show save dialog: %w", err)
		}
		if savePath == "" {
			return "", nil
		}
		v.rememberSavePath(savePath)
		return v.DownloadAttachment(attachmentID, savePath)
	}

	savePath, err := wailsRuntime.SaveFileDialog(v.ctx, wailsRuntime.SaveDialogOptions{
		DefaultDirectory: defaultDir,
		DefaultFilename:  att.Filename,
		Title:            "Save Attachment",
	})
	if err != nil {
		return "", fmt.Errorf("failed to show save dialog: %w", err)
	}
	if savePath == "" {
		return "", nil
	}
	v.rememberSavePath(savePath)

	resultPath, err := v.DownloadAttachment(attachmentID, savePath)
	if err != nil {
		return "", err
	}
	log.Info().Str("attachment", att.Filename).Str("path", resultPath).Msg("Attachment saved")
	return resultPath, nil
}

// openFile opens a file with the system default application.
func (v *ViewerApp) openFile(path string) error {
	if runtime.GOOS == "linux" && platform.IsFlatpak() {
		if platform.IsDocPortalPath(path) {
			wailsRuntime.EventsEmit(v.ctx, "flatpak:filesystem-dialog")
			return nil
		}
		return platform.PortalOpenFile(path)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", path)
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		return platform.OpenPathWindows(path)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// validateOpenPath checks that the path is under an allowed root directory.
func (v *ViewerApp) validateOpenPath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	absPath = filepath.Clean(absPath)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	if v.isRecentSavePath(absPath) {
		return nil
	}

	allowedRoots := []string{
		v.paths.AttachmentsPath(),
		filepath.Join(homeDir, "Downloads"),
		platform.DownloadsDir(),
		v.paths.Data,
	}
	for _, root := range allowedRoots {
		if root == "" {
			continue
		}
		cleanRoot := filepath.Clean(root) + string(filepath.Separator)
		if strings.HasPrefix(absPath, cleanRoot) || absPath == filepath.Clean(root) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside allowed directories", path)
}

// OpenFile opens a file with the system default application.
func (v *ViewerApp) OpenFile(path string) error {
	if err := v.validateOpenPath(path); err != nil {
		return err
	}
	return v.openFile(path)
}

// OpenFolder opens the folder containing a file in the system file manager.
func (v *ViewerApp) OpenFolder(path string) error {
	if err := v.validateOpenPath(path); err != nil {
		return err
	}
	if runtime.GOOS == "linux" && platform.IsFlatpak() {
		return platform.PortalOpenDirectory(path)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	case "darwin":
		cmd = exec.Command("open", "-R", path)
	case "windows":
		cmd = exec.Command("explorer", "/select,", path)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// SaveAllAttachments shows a folder picker and saves all attachments.
func (v *ViewerApp) SaveAllAttachments(messageID string) (string, error) {
	log := logging.WithComponent("viewer")

	attachments, err := v.attachmentStore.GetByMessage(messageID)
	if err != nil {
		return "", fmt.Errorf("failed to get attachments: %w", err)
	}
	if len(attachments) == 0 {
		return "", fmt.Errorf("no attachments found for message")
	}

	defaultDir := platform.DefaultSaveDir()

	if platform.IsFlatpak() && !platform.FlatpakHasWritableHome() {
		wailsRuntime.EventsEmit(v.ctx, "flatpak:filesystem-dialog", "saveAll")
		return "", nil
	}
	if platform.IsFlatpak() {
		return v.saveAllAttachmentsViaPortal(messageID, attachments, defaultDir)
	}

	saveDir, err := wailsRuntime.OpenDirectoryDialog(v.ctx, wailsRuntime.OpenDialogOptions{
		DefaultDirectory: defaultDir,
		Title:            "Save All Attachments",
	})
	if err != nil {
		return "", fmt.Errorf("failed to show folder dialog: %w", err)
	}
	if saveDir == "" {
		return "", nil
	}
	v.rememberSavePath(saveDir)

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return "", fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return "", fmt.Errorf("message not found: %s", messageID)
	}

	raw, err := v.syncEngine.FetchRawMessage(v.ctx, msg.AccountID, msg.FolderID, msg.UID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch message: %w", err)
	}

	downloader := email.NewAttachmentDownloader(v.paths.AttachmentsPath())
	savedCount := 0
	failed := []string{}
	usedNames := map[string]bool{}
	for _, att := range attachments {
		content, err := downloader.ExtractAttachmentContentFor(raw, att)
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to extract attachment")
			failed = append(failed, att.Filename)
			continue
		}
		savePath := filepath.Join(saveDir, email.DeduplicateFilename(saveDir, att.Filename, usedNames))
		_, err = downloader.SaveAttachment(att, content, savePath)
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to save attachment")
			failed = append(failed, att.Filename)
			continue
		}
		savedCount++
	}

	log.Info().Int("count", savedCount).Str("folder", saveDir).Msg("Saved all attachments")
	return saveDir, saveAllResultError(savedCount, len(attachments), failed)
}

// decryptMessageBody decrypts an encrypted message's raw body.
func (v *ViewerApp) decryptMessageBody(msg *message.Message) ([]byte, error) {
	recipientEmail := v.findRecipientIdentityEmail(msg)

	if msg.HasSMIME {
		rawBody, err := v.messageStore.GetSMIMERawBody(msg.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get S/MIME raw body: %w", err)
		}
		if rawBody == nil {
			return nil, fmt.Errorf("no S/MIME raw body for message: %s", msg.ID)
		}
		innerBytes := rawBody
		if msg.SMIMEEncrypted {
			decrypted, _, decErr := v.smimeDecryptor.DecryptMessage(msg.AccountID, recipientEmail, rawBody)
			if decErr != nil {
				return nil, fmt.Errorf("S/MIME decryption failed: %w", decErr)
			}
			innerBytes = decrypted
		}
		ct := extractContentType(innerBytes)
		if smime.IsSMIMESigned(ct) {
			_, unwrapped := v.smimeVerifier.VerifyAndUnwrap(innerBytes)
			if unwrapped != nil {
				innerBytes = unwrapped
			}
		}
		return innerBytes, nil
	}

	if msg.HasPGP {
		rawBody, err := v.messageStore.GetPGPRawBody(msg.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get PGP raw body: %w", err)
		}
		if rawBody == nil {
			return nil, fmt.Errorf("no PGP raw body for message: %s", msg.ID)
		}
		innerBytes := rawBody
		if msg.PGPEncrypted {
			decrypted, _, decErr := v.pgpDecryptor.DecryptMessage(msg.AccountID, recipientEmail, rawBody)
			if decErr != nil {
				return nil, fmt.Errorf("PGP decryption failed: %w", decErr)
			}
			innerBytes = decrypted
		}
		ct := extractContentType(innerBytes)
		if pgp.IsPGPSigned(ct) {
			_, unwrapped := v.pgpVerifier.VerifyAndUnwrap(innerBytes)
			if unwrapped != nil {
				innerBytes = unwrapped
			}
		}
		return innerBytes, nil
	}

	return nil, fmt.Errorf("message %s is not encrypted", msg.ID)
}

// DownloadEncryptedAttachment decrypts an encrypted message, extracts a
// specific attachment, and saves it to disk.
func (v *ViewerApp) DownloadEncryptedAttachment(messageID, filename, savePath string) (string, error) {
	log := logging.WithComponent("viewer")

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return "", fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return "", fmt.Errorf("message not found: %s", messageID)
	}

	innerBytes, err := v.decryptMessageBody(msg)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt message: %w", err)
	}

	downloader := email.NewAttachmentDownloader(v.paths.AttachmentsPath())
	content, err := downloader.ExtractAttachmentContentFor(innerBytes, &message.Attachment{MessageID: messageID, Filename: filename})
	if err != nil {
		return "", fmt.Errorf("failed to extract attachment from decrypted message: %w", err)
	}

	att := &message.Attachment{
		MessageID:   messageID,
		Filename:    filename,
		ContentType: "application/octet-stream",
		Size:        len(content),
	}

	localPath, err := downloader.SaveAttachment(att, content, savePath)
	if err != nil {
		return "", fmt.Errorf("failed to save attachment: %w", err)
	}

	log.Info().Str("attachment", filename).Str("path", localPath).Int("size", len(content)).Msg("Encrypted attachment downloaded")
	return localPath, nil
}

// SaveEncryptedAttachmentAs shows a Save As dialog and saves an attachment
// from an encrypted message.
func (v *ViewerApp) SaveEncryptedAttachmentAs(messageID, filename string) (string, error) {
	defaultDir := platform.DefaultSaveDir()

	if platform.IsFlatpak() {
		savePath, err := platform.PortalSaveFile("Save Attachment", filename, defaultDir)
		if err != nil {
			return "", fmt.Errorf("failed to show save dialog: %w", err)
		}
		if savePath == "" {
			return "", nil
		}
		v.rememberSavePath(savePath)
		return v.DownloadEncryptedAttachment(messageID, filename, savePath)
	}

	savePath, err := wailsRuntime.SaveFileDialog(v.ctx, wailsRuntime.SaveDialogOptions{
		DefaultDirectory: defaultDir,
		DefaultFilename:  filename,
		Title:            "Save Attachment",
	})
	if err != nil {
		return "", fmt.Errorf("failed to show save dialog: %w", err)
	}
	if savePath == "" {
		return "", nil
	}
	v.rememberSavePath(savePath)

	return v.DownloadEncryptedAttachment(messageID, filename, savePath)
}

// OpenEncryptedAttachment decrypts an encrypted message, extracts and opens
// an attachment.
func (v *ViewerApp) OpenEncryptedAttachment(messageID, filename string) error {
	localPath, err := v.DownloadEncryptedAttachment(messageID, filename, "")
	if err != nil {
		return err
	}
	return v.openFile(localPath)
}

// SaveAllEncryptedAttachments shows a folder picker and saves all
// attachments from an encrypted message.
func (v *ViewerApp) SaveAllEncryptedAttachments(messageID string) (string, error) {
	log := logging.WithComponent("viewer")

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return "", fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return "", fmt.Errorf("message not found: %s", messageID)
	}

	innerBytes, err := v.decryptMessageBody(msg)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt message: %w", err)
	}

	parsed := v.syncEngine.ParseDecryptedBody(innerBytes, messageID)
	var regularAtts []*message.Attachment
	for _, att := range parsed.Attachments {
		if !att.IsInline {
			regularAtts = append(regularAtts, att)
		}
	}
	if len(regularAtts) == 0 {
		return "", fmt.Errorf("no attachments found in encrypted message")
	}

	defaultDir := platform.DefaultSaveDir()

	if platform.IsFlatpak() && !platform.FlatpakHasWritableHome() {
		wailsRuntime.EventsEmit(v.ctx, "flatpak:filesystem-dialog", "saveAll")
		return "", nil
	}
	if platform.IsFlatpak() {
		return v.saveAllEncryptedAttachmentsViaPortal(regularAtts, innerBytes, defaultDir)
	}

	saveDir, err := wailsRuntime.OpenDirectoryDialog(v.ctx, wailsRuntime.OpenDialogOptions{
		DefaultDirectory: defaultDir,
		Title:            "Save All Attachments",
	})
	if err != nil {
		return "", fmt.Errorf("failed to show folder dialog: %w", err)
	}
	if saveDir == "" {
		return "", nil
	}
	v.rememberSavePath(saveDir)

	downloader := email.NewAttachmentDownloader(v.paths.AttachmentsPath())
	savedCount := 0
	failed := []string{}
	usedNames := map[string]bool{}
	for _, att := range regularAtts {
		content, err := downloader.ExtractAttachmentContentFor(innerBytes, att)
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to extract encrypted attachment")
			failed = append(failed, att.Filename)
			continue
		}
		savePath := filepath.Join(saveDir, email.DeduplicateFilename(saveDir, att.Filename, usedNames))
		_, err = downloader.SaveAttachment(att, content, savePath)
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to save encrypted attachment")
			failed = append(failed, att.Filename)
			continue
		}
		savedCount++
	}

	log.Info().Int("count", savedCount).Str("folder", saveDir).Msg("Saved all encrypted attachments")
	return saveDir, saveAllResultError(savedCount, len(regularAtts), failed)
}

// saveAllAttachmentsViaPortal saves all attachments using the XDG
// FileChooser portal.
func (v *ViewerApp) saveAllAttachmentsViaPortal(messageID string, attachments []*message.Attachment, defaultDir string) (string, error) {
	log := logging.WithComponent("viewer")

	usedNames := map[string]bool{}
	filenames := make([]string, len(attachments))
	for i, att := range attachments {
		filenames[i] = email.DeduplicateFilename("", att.Filename, usedNames)
	}

	savePaths, err := platform.PortalSaveFiles("Save All Attachments", filenames, defaultDir)
	if err != nil {
		return "", fmt.Errorf("failed to show save dialog: %w", err)
	}
	if len(savePaths) == 0 {
		return "", nil
	}
	for _, p := range savePaths {
		v.rememberSavePath(p)
	}

	msg, err := v.messageStore.Get(messageID)
	if err != nil {
		return "", fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return "", fmt.Errorf("message not found: %s", messageID)
	}

	raw, err := v.syncEngine.FetchRawMessage(v.ctx, msg.AccountID, msg.FolderID, msg.UID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch message: %w", err)
	}

	downloader := email.NewAttachmentDownloader(v.paths.AttachmentsPath())
	savedCount := 0
	failed := []string{}
	for i, att := range attachments {
		if i >= len(savePaths) {
			break
		}
		content, err := downloader.ExtractAttachmentContentFor(raw, att)
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to extract attachment")
			failed = append(failed, att.Filename)
			continue
		}
		_, err = downloader.SaveAttachment(att, content, savePaths[i])
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to save attachment")
			failed = append(failed, att.Filename)
			continue
		}
		savedCount++
	}

	log.Info().Int("count", savedCount).Msg("Saved all attachments via portal")
	return filepath.Dir(savePaths[0]), saveAllResultError(savedCount, len(attachments), failed)
}

// saveAllEncryptedAttachmentsViaPortal saves all encrypted attachments
// using the XDG FileChooser portal.
func (v *ViewerApp) saveAllEncryptedAttachmentsViaPortal(attachments []*message.Attachment, innerBytes []byte, defaultDir string) (string, error) {
	log := logging.WithComponent("viewer")

	usedNames := map[string]bool{}
	filenames := make([]string, len(attachments))
	for i, att := range attachments {
		filenames[i] = email.DeduplicateFilename("", att.Filename, usedNames)
	}

	savePaths, err := platform.PortalSaveFiles("Save All Attachments", filenames, defaultDir)
	if err != nil {
		return "", fmt.Errorf("failed to show save dialog: %w", err)
	}
	if len(savePaths) == 0 {
		return "", nil
	}
	for _, p := range savePaths {
		v.rememberSavePath(p)
	}

	downloader := email.NewAttachmentDownloader(v.paths.AttachmentsPath())
	savedCount := 0
	failed := []string{}
	for i, att := range attachments {
		if i >= len(savePaths) {
			break
		}
		content, err := downloader.ExtractAttachmentContentFor(innerBytes, att)
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to extract encrypted attachment")
			failed = append(failed, att.Filename)
			continue
		}
		_, err = downloader.SaveAttachment(att, content, savePaths[i])
		if err != nil {
			log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to save encrypted attachment")
			failed = append(failed, att.Filename)
			continue
		}
		savedCount++
	}

	log.Info().Int("count", savedCount).Msg("Saved all encrypted attachments via portal")
	return filepath.Dir(savePaths[0]), saveAllResultError(savedCount, len(attachments), failed)
}

// rememberSavePath records a path the user chose in a save dialog (#474).
func (v *ViewerApp) rememberSavePath(p string) {
	if p == "" {
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return
	}
	v.recentSaveMu.Lock()
	defer v.recentSaveMu.Unlock()
	if v.recentSavePaths == nil {
		v.recentSavePaths = make(map[string]struct{})
	}
	v.recentSavePaths[filepath.Clean(abs)] = struct{}{}
}

// isRecentSavePath reports whether absPath is a remembered save path or lies
// under a remembered save directory.
func (v *ViewerApp) isRecentSavePath(absPath string) bool {
	v.recentSaveMu.Lock()
	defer v.recentSaveMu.Unlock()
	for p := range v.recentSavePaths {
		if absPath == p || strings.HasPrefix(absPath, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
