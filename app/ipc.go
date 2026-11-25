package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/ipc"
	"github.com/absonsnet/daak/internal/logging"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ============================================================================
// IPC for Multi-Window Support (Detachable Composer)
// ============================================================================

// initIPC initializes the IPC server for multi-window communication.
// This allows detached composer windows to communicate with the main window.
func (a *App) initIPC(ctx context.Context) {
	log := logging.WithComponent("app.ipc")

	// Create token manager for secure client authentication
	tokenMgr, err := ipc.NewTokenManager()
	if err != nil {
		log.Error().Err(err).Msg("Failed to create IPC token manager")
		return
	}
	a.ipcTokenMgr = tokenMgr

	// Create platform-specific IPC server
	a.ipcServer = ipc.NewServer(tokenMgr)

	// Register message handler
	a.ipcServer.OnMessage(a.handleIPCMessage)

	// Log the address (available immediately after NewServer)
	log.Info().Str("address", a.ipcServer.Address()).Msg("Starting IPC server")

	// Start server in background
	go func() {
		defer recoverPanic("app.ipc", "IPC server")
		if err := a.ipcServer.Start(ctx); err != nil {
			// Context cancellation is expected during shutdown
			if ctx.Err() == nil {
				log.Error().Err(err).Msg("IPC server error")
			}
		}
	}()
}

// handleIPCMessage processes messages received from composer windows.
func (a *App) handleIPCMessage(clientID string, msg ipc.Message) {
	log := logging.WithComponent("app.ipc")

	log.Debug().
		Str("clientID", clientID).
		Str("type", msg.Type).
		Msg("Received IPC message")

	switch msg.Type {
	case ipc.TypeMessageSent:
		var payload ipc.MessageSentPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Error().Err(err).Msg("Failed to parse message_sent payload")
			return
		}
		a.handleComposerMessageSent(payload)

	case ipc.TypeMessageQueued:
		var payload ipc.MessageQueuedPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Error().Err(err).Msg("Failed to parse message_queued payload")
			return
		}
		a.handleComposerMessageQueued(payload)

	case ipc.TypeDraftSaved:
		var payload ipc.DraftSavedPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Error().Err(err).Msg("Failed to parse draft_saved payload")
			return
		}
		a.handleComposerDraftSaved(payload)

	case ipc.TypeDraftDeleted:
		var payload ipc.DraftDeletedPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Error().Err(err).Msg("Failed to parse draft_deleted payload")
			return
		}
		a.handleComposerDraftDeleted(payload)

	case ipc.TypeComposerReady:
		log.Info().Str("clientID", clientID).Msg("Composer window ready")

	case ipc.TypeComposerClosed:
		var payload ipc.ComposerClosedPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Warn().Err(err).Msg("Failed to parse composer_closed payload")
		}
		log.Info().Str("clientID", clientID).Msg("Composer window closed")

	case ipc.TypeViewerReady:
		log.Info().Str("clientID", clientID).Msg("Viewer popup ready")

	case ipc.TypeViewerClosed:
		log.Info().Str("clientID", clientID).Msg("Viewer popup closed")

	case ipc.TypeViewerSwitch:
		var payload ipc.ViewerSwitchPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Error().Err(err).Msg("Failed to parse viewer_switch payload")
			return
		}
		// The popup closes itself after sending; the main window just
		// opens the message in the requested mode.
		wailsRuntime.EventsEmit(a.ctx, "viewer:switch", map[string]interface{}{
			"mode":      payload.Mode,
			"accountId": payload.AccountID,
			"folderId":  payload.FolderID,
			"threadId":  payload.ThreadID,
		})

	case ipc.TypeViewerFolderCountsChanged:
		var payload ipc.ViewerFolderCountsChangedPayload
		if err := msg.ParsePayload(&payload); err != nil {
			log.Error().Err(err).Msg("Failed to parse viewer_folder_counts_changed payload")
			return
		}
		a.handleViewerFolderCountsChanged(payload)

	default:
		log.Warn().Str("type", msg.Type).Msg("Unknown IPC message type")
	}
}

// handleViewerFolderCountsChanged re-reads the folders a viewer popup
// just adjusted (the popup shares the database but lives in another
// process, so the main window's counts are stale until told) and
// emits folders:countsChanged so the sidebar badge and the OS badge
// refresh (fourth-audit finding 47). The rows are re-read from the
// database rather than trusting the payload — the payload is the
// signal, the database is the source.
func (a *App) handleViewerFolderCountsChanged(payload ipc.ViewerFolderCountsChangedPayload) {
	counts := make(map[string]int, len(payload.Counts))
	for folderID := range payload.Counts {
		if f, err := a.folderStore.Get(folderID); err == nil && f != nil {
			counts[folderID] = f.UnreadCount
		}
	}
	a.emitFolderCountsChanged(counts)
}

// handleComposerMessageQueued is called when a detached composer
// queues a message for background send (Undo Send). The composer
// closes right away, so the main window owns the countdown toast:
// emit the event the frontend's undo-send store listens for. The
// main process's own worker dispatches the message when the window
// elapses — nothing else to do here.
func (a *App) handleComposerMessageQueued(payload ipc.MessageQueuedPayload) {
	log := logging.WithComponent("app.ipc")

	log.Info().
		Str("queueID", payload.QueueID).
		Str("accountID", payload.AccountID).
		Int("delaySeconds", payload.DelaySeconds).
		Msg("Composer queued message notification")

	wailsRuntime.EventsEmit(a.ctx, "sendqueue:queued", map[string]interface{}{
		"queueId":      payload.QueueID,
		"accountId":    payload.AccountID,
		"draftId":      payload.DraftID,
		"subject":      payload.Subject,
		"recipient":    payload.Recipient,
		"delaySeconds": payload.DelaySeconds,
	})
}

// handleComposerMessageSent is called when a composer successfully sends a message.
// Emits an event to the main window frontend to show a toast and refresh folders.
func (a *App) handleComposerMessageSent(payload ipc.MessageSentPayload) {
	log := logging.WithComponent("app.ipc")

	log.Info().
		Str("accountID", payload.AccountID).
		Int64("folderID", payload.FolderID).
		Msg("Composer sent message notification")

	// Emit event to frontend for toast notification and folder refresh
	wailsRuntime.EventsEmit(a.ctx, "composer:messageSent", map[string]interface{}{
		"accountId": payload.AccountID,
		"folderId":  payload.FolderID,
	})

	// Sync Sent folder to pick up the new message
	go func() {
		defer recoverPanic("app.ipc", "sync sent folder")
		if err := a.syncSentFolder(payload.AccountID); err != nil {
			log.Warn().Err(err).Msg("Failed to sync Sent folder after composer send")
		}
	}()
}

// handleComposerDraftSaved is called when a composer saves a draft.
// The composer window handles its own IMAP sync directly. We sync the Drafts
// folder here so the main window's folder view shows the newly uploaded draft.
func (a *App) handleComposerDraftSaved(payload ipc.DraftSavedPayload) {
	log := logging.WithComponent("app.ipc")

	log.Info().
		Str("accountID", payload.AccountID).
		Str("draftID", payload.DraftID).
		Msg("Composer saved draft notification")

	// Sync the Drafts folder to pick up the newly uploaded draft.
	// Refresh signal to the main window's MessageList rides on the folder:synced
	// event emitted by SyncFolder — same path as the in-window save case.
	// The notification is sent after the composer's IMAP upload completes,
	// so we can sync immediately
	go func() {
		defer recoverPanic("app.ipc", "sync drafts folder")
		draftsFolder, err := a.GetSpecialFolder(payload.AccountID, folder.TypeDrafts)
		if err != nil || draftsFolder == nil {
			log.Warn().Err(err).Str("accountID", payload.AccountID).Msg("Could not find Drafts folder for sync")
			return
		}

		if err := a.SyncFolder(payload.AccountID, draftsFolder.ID); err != nil {
			log.Warn().Err(err).Str("folderID", draftsFolder.ID).Msg("Failed to sync Drafts folder")
			return
		}

		log.Debug().Str("folderID", draftsFolder.ID).Msg("Synced Drafts folder after composer draft save")
	}()
}

// handleComposerDraftDeleted is called when a composer deletes a draft.
// Emits messages:updated for immediate UI refresh and syncs the Drafts folder
// so the main window's folder view reflects the deletion.
func (a *App) handleComposerDraftDeleted(payload ipc.DraftDeletedPayload) {
	log := logging.WithComponent("app.ipc")

	log.Info().
		Str("accountID", payload.AccountID).
		Msg("Composer deleted draft notification")

	draftsFolder, err := a.GetSpecialFolder(payload.AccountID, folder.TypeDrafts)
	if err != nil || draftsFolder == nil {
		log.Warn().Err(err).Str("accountID", payload.AccountID).Msg("Could not find Drafts folder for sync")
		return
	}

	// Notify frontend to refresh the message list immediately
	wailsRuntime.EventsEmit(a.ctx, "messages:updated", map[string]interface{}{
		"accountId": payload.AccountID,
		"folderId":  draftsFolder.ID,
	})

	// Sync the Drafts folder in background to reconcile with IMAP
	go func() {
		defer recoverPanic("app.ipc", "sync drafts folder")
		if err := a.SyncFolder(payload.AccountID, draftsFolder.ID); err != nil {
			log.Warn().Err(err).Str("folderID", draftsFolder.ID).Msg("Failed to sync Drafts folder after deletion")
			return
		}
		log.Debug().Str("folderID", draftsFolder.ID).Msg("Synced Drafts folder after composer draft delete")
	}()
}

// OpenComposerWindow spawns a new detached composer window.
// This creates a separate process of the same executable with composer mode flags.
// If mailtoURL is non-empty, the --mailto flag is appended for the composer to handle.
func (a *App) OpenComposerWindow(accountID, mode, messageID, draftID, mailtoURL string) error {
	log := logging.WithComponent("app.ipc")

	if a.ipcServer == nil || a.ipcTokenMgr == nil {
		return nil // IPC not initialized - not an error, just skip
	}

	execPath, err := os.Executable()
	if err != nil {
		return nil // Can't get executable path - skip
	}

	// Build command line arguments
	args := []string{
		"--compose",
		"--account", accountID,
		"--ipc-address", a.ipcServer.Address(),
	}

	// Add mode-specific arguments
	if draftID != "" {
		args = append(args, "--draft-id", draftID)
	} else if mode != "" && mode != "new" {
		args = append(args, "--mode", mode)
		if messageID != "" {
			args = append(args, "--message-id", messageID)
		}
	}

	// Add mailto URL if provided
	if mailtoURL != "" {
		args = append(args, "--mailto", mailtoURL)
	}

	// Forward the parent's logging options so a capture session covers the
	// composer too. The composer gets its own file: two processes rotating
	// one file would race.
	if lvl := logging.LevelName(); lvl != "" {
		args = append(args, "--log-level", lvl)
	}
	if f := logging.FilePath(); f != "" {
		args = append(args, "--log-file", composerLogPath(f))
	}

	log.Info().
		Str("execPath", execPath).
		Strs("args", args).
		Msg("Spawning composer window")

	cmd := exec.Command(execPath, args...)

	// Pass token securely via stdin
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil // Can't create stdin pipe - skip
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		return nil // Can't start process - skip
	}

	// Write token to stdin and close immediately
	go func() {
		defer recoverPanic("app.ipc", "write IPC token")
		token := a.ipcTokenMgr.GetToken()
		_, _ = stdin.Write([]byte(token))
		stdin.Close()
	}()

	log.Info().Int("pid", cmd.Process.Pid).Msg("Composer window spawned")

	return nil
}

// OpenViewerWindow spawns a detached message-viewer popup window for one
// thread. Mirrors OpenComposerWindow: a separate process of the same
// executable with --view-message flags, authenticated to the IPC server
// with the same token the composer windows use.
func (a *App) OpenViewerWindow(accountID, folderID, threadID string) error {
	log := logging.WithComponent("app.ipc")

	if a.ipcServer == nil || a.ipcTokenMgr == nil {
		return nil // IPC not initialized - not an error, just skip
	}

	execPath, err := os.Executable()
	if err != nil {
		return nil // Can't get executable path - skip
	}

	args := []string{
		"--view-message",
		"--account", accountID,
		"--folder-id", folderID,
		"--thread-id", threadID,
		"--ipc-address", a.ipcServer.Address(),
	}

	// Forward the parent's logging options so a capture session covers the
	// viewer popup too, on its own file like the composer.
	if lvl := logging.LevelName(); lvl != "" {
		args = append(args, "--log-level", lvl)
	}
	if f := logging.FilePath(); f != "" {
		args = append(args, "--log-file", viewerLogPath(f))
	}

	log.Info().
		Str("execPath", execPath).
		Strs("args", args).
		Msg("Spawning viewer popup window")

	cmd := exec.Command(execPath, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil // Can't create stdin pipe - skip
	}

	if err := cmd.Start(); err != nil {
		return nil // Can't start process - skip
	}

	go func() {
		defer recoverPanic("app.ipc", "write IPC token")
		token := a.ipcTokenMgr.GetToken()
		_, _ = stdin.Write([]byte(token))
		stdin.Close()
	}()

	log.Info().Int("pid", cmd.Process.Pid).Msg("Viewer popup spawned")

	return nil
}

// BroadcastThemeChange notifies all composer windows that the theme has changed.
func (a *App) BroadcastThemeChange(theme string) {
	if a.ipcServer == nil {
		return
	}

	msg, err := ipc.NewMessage(ipc.TypeThemeChanged, ipc.ThemeChangedPayload{
		Theme: theme,
	})
	if err != nil {
		return
	}

	_ = a.ipcServer.Broadcast(msg)
}

// GetIPCAddress returns the IPC server address (for testing/debugging).
func (a *App) GetIPCAddress() string {
	if a.ipcServer == nil {
		return ""
	}
	return a.ipcServer.Address()
}

// GetConnectedComposers returns the number of connected composer windows.
func (a *App) GetConnectedComposers() int {
	if a.ipcServer == nil {
		return 0
	}
	return len(a.ipcServer.Clients())
}

// composerLogPath derives the detached composer's log file from the main
// log file: daak.log → daak.composer.log (no extension → daak.composer).
func composerLogPath(mainPath string) string {
	ext := filepath.Ext(mainPath)
	return strings.TrimSuffix(mainPath, ext) + ".composer" + ext
}

// viewerLogPath derives the viewer popup's log file the same way:
// daak.log → daak.viewer.log.
func viewerLogPath(mainPath string) string {
	ext := filepath.Ext(mainPath)
	return strings.TrimSuffix(mainPath, ext) + ".viewer" + ext
}
