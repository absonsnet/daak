package app

import (
	"context"
	"fmt"
	"time"

	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/imap"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/undo"
	goImap "github.com/emersion/go-imap/v2"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// withIMAPRetry wraps an IMAP operation with stale-connection retry.
// If the operation fails with a connection error, the dead connection is discarded
// and the operation is retried once with a fresh connection.
func (a *App) withIMAPRetry(accountID string, op func(conn *imap.Client) error) error {
	log := logging.WithComponent("app.imapRetry")

	poolConn, err := a.imapPool.GetConnection(a.ctx, accountID)
	if err != nil {
		return fmt.Errorf("failed to get IMAP connection: %w", err)
	}

	err = op(poolConn.Client())
	if err == nil {
		a.imapPool.Release(poolConn)
		return nil
	}

	if !imap.IsConnectionError(err) {
		a.imapPool.Release(poolConn)
		return err
	}

	// Stale connection — discard and retry once with fresh connection
	log.Warn().Err(err).Str("account", accountID).Msg("IMAP connection error, retrying with fresh connection")
	a.imapPool.Discard(poolConn)

	poolConn, err = a.imapPool.GetConnection(a.ctx, accountID)
	if err != nil {
		return fmt.Errorf("failed to get IMAP connection on retry: %w", err)
	}
	defer a.imapPool.Release(poolConn)

	return op(poolConn.Client())
}

// ============================================================================
// Message Actions API - Exposed to frontend via Wails bindings
// ============================================================================

// MarkAsRead marks messages as read
// expandToFolderCopies widens an action's target ids to every same-folder row
// sharing a Message-ID with them (server duplicate deliveries). The viewer shows
// those copies as one merged message, so the user's action must cover all of
// them. On lookup failure the action proceeds on the original ids.
func (a *App) expandToFolderCopies(messageIDs []string) []string {
	expanded, err := a.messageStore.ExpandToFolderCopies(messageIDs)
	if err != nil {
		log := logging.WithComponent("app")
		log.Warn().Err(err).Msg("Failed to expand action to duplicate copies")
		return messageIDs
	}
	return expanded
}

func (a *App) MarkAsRead(messageIDs []string) error {
	return a.setReadStatus(messageIDs, true)
}

// MarkAllFolderMessagesAsRead marks all unread messages in a folder as read
func (a *App) MarkAllFolderMessagesAsRead(folderID string) error {
	ids, err := a.messageStore.GetUnreadMessageIDsByFolder(folderID)
	if err != nil {
		return fmt.Errorf("failed to get unread messages: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	return a.MarkAsRead(ids)
}

// MarkAllFolderMessagesAsUnread marks all read messages in a folder as unread
func (a *App) MarkAllFolderMessagesAsUnread(folderID string) error {
	ids, err := a.messageStore.GetReadMessageIDsByFolder(folderID)
	if err != nil {
		return fmt.Errorf("failed to get read messages: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	return a.MarkAsUnread(ids)
}

// GetMarkAllCoverage reports how many messages a whole-folder mark as read
// (seen=true) or unread (seen=false) would reach beyond the local sync
// window: the folder's server-side counts (STATUS, whole mailbox) minus the
// rows Daak holds locally. Zero means the local-only action already covers
// everything; the UI asks before going server-wide otherwise (#458).
func (a *App) GetMarkAllCoverage(folderID string, seen bool) (int, error) {
	f, err := a.folderStore.Get(folderID)
	if err != nil {
		return 0, fmt.Errorf("failed to get folder: %w", err)
	}
	if f == nil {
		return 0, fmt.Errorf("folder not found: %s", folderID)
	}

	var server, local int
	switch seen {
	case true:
		server = f.UnreadCount
		local, err = a.messageStore.CountUnreadByFolder(folderID)
	default:
		server = f.TotalCount - f.UnreadCount
		local, err = a.messageStore.CountReadByFolder(folderID)
	}
	if err != nil {
		return 0, err
	}
	if extra := server - local; extra > 0 {
		return extra, nil
	}
	return 0, nil
}

// MarkWholeFolder marks every message in the folder read (seen=true) or
// unread (seen=false) on the server, not just the rows inside the sync
// window. Local rows flip first so the list updates at once; then one
// UID STORE 1:* on the server; then the folder's counts are refreshed from
// STATUS so the sidebar badge reflects the server (#458).
func (a *App) MarkWholeFolder(folderID string, seen bool) error {
	log := logging.WithComponent("app")

	var localErr error
	switch seen {
	case true:
		localErr = a.MarkAllFolderMessagesAsRead(folderID)
	default:
		localErr = a.MarkAllFolderMessagesAsUnread(folderID)
	}
	if localErr != nil {
		return localErr
	}

	f, err := a.folderStore.Get(folderID)
	if err != nil {
		return fmt.Errorf("failed to get folder: %w", err)
	}
	if f == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}

	var status *imap.Mailbox
	err = a.withIMAPRetry(f.AccountID, func(conn *imap.Client) error {
		if _, selErr := conn.SelectMailbox(a.ctx, f.Path); selErr != nil {
			return selErr
		}
		before, stErr := conn.GetMailboxStatus(a.ctx, f.Path)
		if stErr != nil {
			return stErr
		}
		if before.Messages > 0 {
			if flagErr := conn.SetFlagsOnAllMessages([]goImap.Flag{goImap.FlagSeen}, seen); flagErr != nil {
				return flagErr
			}
		}
		after, stErr := conn.GetMailboxStatus(a.ctx, f.Path)
		if stErr != nil {
			return stErr
		}
		status = after
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to mark folder on server: %w", err)
	}

	f.UnreadCount = int(status.Unseen)
	f.TotalCount = int(status.Messages)
	if err := a.folderStore.Update(f); err != nil {
		log.Warn().Err(err).Str("folderID", folderID).Msg("Failed to update folder counts after whole-folder mark")
	}
	a.emitFolderCounts(map[string]int{folderID: f.UnreadCount})

	log.Info().
		Str("folder", f.Path).
		Bool("seen", seen).
		Int("unread", f.UnreadCount).
		Msg("Marked whole folder on server")
	return nil
}

// MarkAsUnread marks messages as unread
func (a *App) MarkAsUnread(messageIDs []string) error {
	return a.setReadStatus(messageIDs, false)
}

func (a *App) setReadStatus(messageIDs []string, isRead bool) error {
	log := logging.WithComponent("app")

	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	// Get messages to find their UIDs and folders
	messages, err := a.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}

	// Group by folder for IMAP operations
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	// Update local DB first (local-first)
	isReadPtr := &isRead
	if err := a.messageStore.UpdateFlagsBatch(messageIDs, isReadPtr, nil); err != nil {
		return fmt.Errorf("failed to update local flags: %w", err)
	}

	// Record the change as pending before pushing: if the push fails,
	// the row survives, the next sync must not overwrite this flag
	// (UpdateFlagsByUIDBatch guards pending flags), and the change is
	// re-pushed after a sync or on demand (finding 18).
	if err := a.messageStore.RecordPendingFlagChanges(messages, message.FlagTypeRead, isRead); err != nil {
		log.Warn().Err(err).Msg("Local read flag updated but its pending row could not be recorded — a failed push may flip it back")
	}

	// Emit event for UI update with flag state
	wailsRuntime.EventsEmit(a.ctx, "messages:readChanged", map[string]interface{}{
		"messageIds": messageIDs,
		"isRead":     isRead,
	})

	// Update folder unread counts in background to avoid blocking other DB
	// operations. Deltas, not a recount — see folder_counts.go.
	go func() {
		defer recoverPanic("app.actions", "update folder counts")
		folderCounts := make(map[string]int)
		for folderID, msgs := range byFolder {
			a.adjustFolderCounts(folderCounts, folderID, 0, readFlipDelta(msgs, isRead))
		}
		a.emitFolderCounts(folderCounts)
	}()

	// Sync to IMAP in background with retry
	go func() {
		defer recoverPanic("app.actions", "sync flags to IMAP")
		pushFlagChangesByFolder(a.messageStore, a.syncFlagsToIMAP, func(event string, data map[string]interface{}) {
			wailsRuntime.EventsEmit(a.ctx, event, data)
		}, byFolder, message.FlagTypeRead, isRead)
	}()

	// Read-flag changes are intentionally NOT pushed onto the undo stack.
	// The original design treated every mark-read/unread as undoable, but
	// in practice the auto-mark-as-read timer (and bulk operations) flooded
	// the stack so Cmd+Z routinely undid a background read flip instead of
	// the user's last real action (archive / move / trash). The cure is
	// simpler than per-call-site filtering: just don't make read changes
	// undoable at all — the user can re-flip manually if they need to.
	// Closes #243.

	return nil
}

// Star marks messages as starred
func (a *App) Star(messageIDs []string) error {
	return a.setStarredStatus(messageIDs, true)
}

// Unstar removes star from messages
func (a *App) Unstar(messageIDs []string) error {
	return a.setStarredStatus(messageIDs, false)
}

func (a *App) setStarredStatus(messageIDs []string, isStarred bool) error {
	log := logging.WithComponent("app")

	if len(messageIDs) == 0 {
		return nil
	}
	// Star/unstar covers every same-folder copy sharing a Message-ID,
	// exactly like read/unread already does (fourth-audit finding 43):
	// the viewer shows the copies as one merged message, so starring
	// one copy must star them all.
	messageIDs = a.expandToFolderCopies(messageIDs)

	messages, err := a.messageStore.GetByIDs(messageIDs)
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

	// Update local DB first
	isStarredPtr := &isStarred
	if err := a.messageStore.UpdateFlagsBatch(messageIDs, nil, isStarredPtr); err != nil {
		return fmt.Errorf("failed to update local flags: %w", err)
	}

	if err := a.messageStore.RecordPendingFlagChanges(messages, message.FlagTypeStarred, isStarred); err != nil {
		log.Warn().Err(err).Msg("Local star flag updated but its pending row could not be recorded — a failed push may flip it back")
	}

	wailsRuntime.EventsEmit(a.ctx, "messages:starredChanged", map[string]interface{}{
		"messageIds": messageIDs,
		"isStarred":  isStarred,
	})

	// Sync to IMAP in background with retry
	go func() {
		defer recoverPanic("app.actions", "sync star flags to IMAP")
		pushFlagChangesByFolder(a.messageStore, a.syncFlagsToIMAP, func(event string, data map[string]interface{}) {
			wailsRuntime.EventsEmit(a.ctx, event, data)
		}, byFolder, message.FlagTypeStarred, isStarred)
	}()

	// Star changes are intentionally NOT pushed onto the undo stack. Same
	// rationale as setReadStatus: the cost of stack pollution outweighs
	// the value of undo for a flag flip that the user can trivially
	// reverse with another star/unstar click. Closes #243.

	return nil
}

// syncFlagsToIMAP syncs flag changes to IMAP server
// --- Pending flag changes (fourth-audit finding 18) -----------------

// pushFlagChangesByFolder pushes local read/star changes for each folder
// to IMAP with retry, deleting the pending rows on success. A push that
// still fails after 3 tries leaves its rows pending — the next sync's
// flag download must not overwrite them (UpdateFlagsByUIDBatch guards
// pending flags), and the UI is told how many changes are still
// un-pushed so it can show the warning and offer a retry. Shared by
// App and ViewerApp, which differ only in how they push and emit.
func pushFlagChangesByFolder(store *message.Store, push func(messages []*message.Message, folderID, flagType string, flagValue bool) error, emit func(event string, data map[string]interface{}), byFolder map[string][]*message.Message, flagType string, flagValue bool) {
	log := logging.WithComponent("app")
	failed := 0
	for folderID, msgs := range byFolder {
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			err = push(msgs, folderID, flagType, flagValue)
			if err == nil {
				break
			}
			log.Warn().Err(err).Int("attempt", attempt).Str("folderID", folderID).Msg("Failed to sync flags to IMAP, retrying...")
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		if err != nil {
			log.Error().Err(err).Str("folderID", folderID).Msg("Failed to sync flags to IMAP after 3 attempts — change stays queued for re-push")
			failed += len(msgs)
			continue
		}
		ids := make([]string, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		if err := store.DeletePendingFlagChanges(ids, flagType); err != nil {
			log.Warn().Err(err).Str("folderID", folderID).Msg("Flag push succeeded but its pending rows could not be cleared")
		}
	}
	if failed > 0 {
		if count, err := store.PendingFlagChangeCount(); err == nil {
			emit("messages:flagsUnsynced", map[string]interface{}{"count": count})
		}
	}
}

// retryPendingFlagChanges re-pushes every un-pushed local flag change,
// grouped by account/folder/flag. Returns how many messages were
// pushed. A group that still fails stays pending for the next attempt.
func (a *App) retryPendingFlagChanges() (int, error) {
	pending, err := a.messageStore.ListPendingFlagChanges()
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	type groupKey struct {
		accountID, folderID, flagType string
	}
	groups := make(map[groupKey][]message.PendingFlagChange)
	for _, p := range pending {
		k := groupKey{p.AccountID, p.FolderID, p.FlagType}
		groups[k] = append(groups[k], p)
	}
	pushed := 0
	var firstErr error
	for k, rows := range groups {
		msgs := make([]*message.Message, len(rows))
		ids := make([]string, len(rows))
		for i, p := range rows {
			msgs[i] = &message.Message{ID: p.MessageID, AccountID: p.AccountID, FolderID: p.FolderID, UID: p.UID}
			ids[i] = p.MessageID
		}
		if err := a.syncFlagsToIMAP(msgs, k.folderID, k.flagType, rows[0].FlagValue); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := a.messageStore.DeletePendingFlagChanges(ids, k.flagType); err != nil {
			log := logging.WithComponent("app")
			log.Warn().Err(err).Msg("Re-pushed flags but their pending rows could not be cleared")
		}
		pushed += len(rows)
	}
	if count, err := a.messageStore.PendingFlagChangeCount(); err == nil {
		wailsRuntime.EventsEmit(a.ctx, "messages:flagsUnsynced", map[string]interface{}{"count": count})
	}
	return pushed, firstErr
}

// RetryPendingFlagChanges re-pushes un-pushed local read/star changes
// to the server (Wails-bound; the "changes not synced" banner calls it).
func (a *App) RetryPendingFlagChanges() (int, error) {
	return a.retryPendingFlagChanges()
}

// GetPendingFlagChangeCount returns how many local read/star changes
// have not reached the server yet (Wails-bound; the banner reads it).
func (a *App) GetPendingFlagChangeCount() (int, error) {
	return a.messageStore.PendingFlagChangeCount()
}

// retryPendingFlagsForAccount re-pushes un-pushed flag changes in the
// background after a sync, when the server is reachable again. It
// retries everything pending (each group is pushed through its own
// account's connection); a group that still fails stays pending.
func (a *App) retryPendingFlagsForAccount(accountID string) {
	if _, err := a.retryPendingFlagChanges(); err != nil {
		log := logging.WithComponent("app")
		log.Warn().Err(err).Str("accountID", accountID).Msg("Re-push of pending flag changes failed; they stay queued")
	}
}

func (a *App) syncFlagsToIMAP(messages []*message.Message, folderID, flagType string, flagValue bool) error {
	if len(messages) == 0 {
		return nil
	}

	folderObj, err := a.folderStore.Get(folderID)
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

	return a.withIMAPRetry(messages[0].AccountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(a.ctx, folderObj.Path); err != nil {
			return fmt.Errorf("failed to select mailbox: %w", err)
		}

		// Stamp before the STORE so the server's IDLE echo of this change is
		// recognized as ours and doesn't trigger a self-inflicted re-sync.
		a.noteOwnFlagChange(messages[0].AccountID)
		if flagValue {
			return conn.AddMessageFlags(uids, []goImap.Flag{flag})
		}
		return conn.RemoveMessageFlags(uids, []goImap.Flag{flag})
	})
}

// MoveToFolder moves messages to a specified folder
// MoveToFolder moves messages to the destination folder. One call is one
// undoable action: whatever the move fans out into (one command per source
// folder, one per account partition, a cross-account Trash), a single Undo
// reverses all of it — see pushUndoCommands.
func (a *App) MoveToFolder(messageIDs []string, destFolderID string) error {
	cmds, destName, err := a.moveToFolderCmds(messageIDs, destFolderID)
	a.pushUndoCommands(destName, cmds)
	return err
}

// moveToFolderCmds performs the move and returns the undo commands it
// created, without pushing them. Callers (MoveToFolder and the
// cross-account helpers) push them as a single undoable action. Commands
// for completed parts are returned alongside an error, so a partial move
// stays undoable.
func (a *App) moveToFolderCmds(messageIDs []string, destFolderID string) ([]undo.Command, string, error) {
	log := logging.WithComponent("app")

	if len(messageIDs) == 0 {
		return nil, "", nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	// Cross-account selections (Unified Inbox or any mixed-account multi-
	// select) route to a partition helper that recurses through this
	// same function with uniform single-account slices. The existing
	// messages[0].AccountID != destFolder.AccountID guard below then
	// fires exactly once per partition with a correct full-batch
	// classification.
	if spans, _ := a.messageStore.SpansMultipleAccounts(messageIDs); spans {
		return a.moveToFolderCrossAccountCmds(messageIDs, destFolderID)
	}

	messages, err := a.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil, "", nil
	}

	destFolder, err := a.folderStore.Get(destFolderID)
	if err != nil || destFolder == nil {
		return nil, "", fmt.Errorf("destination folder not found: %s", destFolderID)
	}

	// Cross-account move: APPEND raw bytes to destination first, then route source
	// cleanup through the existing Trash() pipeline (which handles local DB, IMAP
	// COPY+EXPUNGE within source, folder counts, events, undo, Gmail labels).
	// Strict ordering: APPEND completes synchronously before Trash runs — if
	// APPEND fails, source stays untouched.
	if messages[0].AccountID != destFolder.AccountID {
		if err := a.copyMessagesAcrossAccounts(messages, destFolder); err != nil {
			return nil, destFolder.Name, fmt.Errorf("cross-account move: append failed: %w", err)
		}
		// RFC822 IDs of the moved messages — the undo command uses
		// them to find the destination copy. Messages without one
		// cannot be tracked to the destination account, so their
		// undo falls back to the trash leg alone (the old behaviour).
		var rfc822IDs []string
		for _, m := range messages {
			if m.MessageID != "" {
				rfc822IDs = append(rfc822IDs, m.MessageID)
			}
		}
		_, trashCmds, _, trashErr := a.trashCmds(messageIDs)
		if trashErr != nil {
			// The append succeeded but the source cleanup failed:
			// roll the appended copy back, or the message ends up
			// in both accounts. The destination sync has not run
			// yet, so the copy has no local rows — locate it on the
			// server by Message-ID header and delete it there.
			rollbackErr := a.rollbackCrossAccountAppend(destFolder, rfc822IDs)
			if rollbackErr != nil {
				return trashCmds, destFolder.Name, fmt.Errorf("cross-account move: trash failed: %w (destination copy could not be rolled back: %v)", trashErr, rollbackErr)
			}
			return trashCmds, destFolder.Name, fmt.Errorf("cross-account move: trash failed: %w (destination copy rolled back)", trashErr)
		}
		// Sync destination so appended messages get correct UIDs locally.
		go func() {
			defer recoverPanic("app.actions", "cross-account move dest sync")
			_ = a.SyncFolder(destFolder.AccountID, destFolder.ID)
		}()
		if len(rfc822IDs) == 0 {
			return trashCmds, destFolder.Name, nil
		}
		// One undo command for the whole cross-account move: it
		// restores the source via the trash leg AND deletes the
		// destination copy, so Undo no longer leaves the message in
		// both accounts.
		return []undo.Command{undo.NewCrossAccountMoveCommand(
			a,
			destFolder.AccountID,
			destFolder.ID,
			rfc822IDs,
			trashCmds,
			fmt.Sprintf("Move to %s", destFolder.Name),
		)}, destFolder.Name, nil
	}

	// Group by source folder
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	// Stamp the own-expunge window BEFORE the local mutation — the IMAP
	// goroutine stamps it too, but that runs later; without this, a reconcile
	// firing between the local move and the goroutine start sees no window
	// and can resurrect the just-moved messages
	a.noteOwnExpunge(messages[0].AccountID)

	// Update local DB first
	if err := a.messageStore.MoveMessages(messageIDs, destFolderID); err != nil {
		return nil, "", fmt.Errorf("failed to move messages locally: %w", err)
	}

	wailsRuntime.EventsEmit(a.ctx, "messages:moved", map[string]interface{}{
		"messageIds":   messageIDs,
		"destFolderId": destFolderID,
	})

	// Update folder counts for source and destination folders. Deltas from
	// the pre-move message objects, not a recount — see folder_counts.go.
	go func() {
		defer recoverPanic("app.actions", "update folder counts after move")
		folderCounts := make(map[string]int)
		for folderID, msgs := range byFolder {
			a.adjustFolderCounts(folderCounts, folderID, -len(msgs), -unreadIn(msgs))
		}
		a.adjustFolderCounts(folderCounts, destFolderID, len(messages), unreadIn(messages))
		a.emitFolderCounts(folderCounts)
	}()

	// Sync to IMAP in background (COPY + DELETE), then sync destination to get correct UIDs.
	// Use SyncFolder instead of calling SyncMessages/FetchBodiesInBackground directly
	// so that back-to-back moves to the same folder are serialized — the second call
	// cancels the first and starts fresh, preventing the first sync from deleting
	// locally-moved messages whose IMAP COPY hasn't completed yet.
	go func() {
		defer recoverPanic("app.actions", "move messages on IMAP")
		for sourceFolderID, msgs := range byFolder {
			if err := a.moveMessagesToIMAP(msgs, sourceFolderID, destFolder); err != nil {
				log.Error().Err(err).
					Str("sourceFolderID", sourceFolderID).
					Str("destFolderID", destFolderID).
					Msg("Failed to move messages on IMAP")
				return
			}
		}

		// Sync destination folder so moved messages get correct UIDs (headers + bodies).
		// Clear the debounce timestamp so this request isn't silently dropped.
		if len(messages) > 0 {
			accountID := messages[0].AccountID
			syncKey := accountID + ":" + destFolderID
			a.syncMu.Lock()
			delete(a.syncLastRequest, syncKey)
			a.syncMu.Unlock()

			// Delete the temp rows only once the destination sync has
			// succeeded AND the real server rows are confirmed in the
			// destination folder (re-audit finding R15). Deleting them
			// after a failed sync loses the message's local state even
			// though the server copy reappears later as new.
			if _, err := cleanupMovedTempUIDs(a.messageStore, destFolderID, messages, func() error {
				return a.SyncFolder(accountID, destFolderID)
			}); err != nil {
				log.Warn().Err(err).Str("destFolderID", destFolderID).Msg("Keeping temp UIDs after move: destination not reconciled")
			}
		}
	}()

	// Create one undo command per source folder; the caller pushes them
	// as a single undoable action.
	var cmds []undo.Command
	for sourceFolderID, msgs := range byFolder {
		rfc822IDs := make([]string, 0, len(msgs))
		for _, m := range msgs {
			if m.MessageID != "" {
				rfc822IDs = append(rfc822IDs, m.MessageID)
			}
		}
		if len(rfc822IDs) == 0 {
			continue
		}

		cmd := undo.NewMoveCommand(
			a,
			msgs[0].AccountID,
			rfc822IDs,
			sourceFolderID,
			destFolderID,
			fmt.Sprintf("Move to %s", destFolder.Name),
		)
		cmds = append(cmds, cmd)
	}

	return cmds, destFolder.Name, nil
}

// rollbackCrossAccountAppend deletes the destination copies of a
// cross-account move from the server, locating them by Message-ID
// header. Only used when the source-side trash step failed after a
// successful APPEND: the destination sync has not run yet, so the
// copies have no local rows to resolve UIDs from. A message-ID with
// no server match is already gone and skipped, not an error.
func (a *App) rollbackCrossAccountAppend(destFolder *folder.Folder, rfc822IDs []string) error {
	if len(rfc822IDs) == 0 {
		return fmt.Errorf("no Message-ID headers to locate the appended copy")
	}
	return a.withIMAPRetry(destFolder.AccountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(a.ctx, destFolder.Path); err != nil {
			return fmt.Errorf("failed to select destination mailbox: %w", err)
		}
		for _, id := range rfc822IDs {
			uids, err := conn.SearchUIDsByMessageID(id)
			if err != nil {
				return err
			}
			if len(uids) == 0 {
				continue
			}
			if err := conn.DeleteMessagesByUID(uids); err != nil {
				return fmt.Errorf("failed to delete appended copy of %s: %w", id, err)
			}
		}
		return nil
	})
}

// isGmailAccount checks if the account uses Gmail's IMAP server.
// Gmail uses labels instead of folders — IMAP COPY adds a label rather than
// creating an independent copy, and adding the Trash/Spam label hides the
// message from all other IMAP mailbox views.
func (a *App) isGmailAccount(accountID string) bool {
	acc, err := a.accountStore.Get(accountID)
	if err != nil || acc == nil {
		return false
	}
	return acc.IMAPHost == "imap.gmail.com"
}

func (a *App) moveMessagesToIMAP(messages []*message.Message, sourceFolderID string, destFolder *folder.Folder) error {
	log := logging.WithComponent("app.moveMessagesToIMAP")

	if len(messages) == 0 {
		return nil
	}

	sourceFolder, err := a.folderStore.Get(sourceFolderID)
	if err != nil || sourceFolder == nil {
		return fmt.Errorf("source folder not found")
	}

	// Collect UIDs for logging
	uidList := make([]uint32, len(messages))
	for i, m := range messages {
		uidList[i] = m.UID
	}

	log.Info().
		Str("sourceFolder", sourceFolder.Path).
		Str("destFolder", destFolder.Path).
		Uints32("uids", uidList).
		Int("count", len(messages)).
		Msg("Starting IMAP move operation")

	accountID := messages[0].AccountID
	isGmail := a.isGmailAccount(accountID)
	destIsTrashOrSpam := destFolder.Type == folder.TypeTrash || destFolder.Type == folder.TypeSpam

	// For Gmail + dest is Trash/Spam: partition messages by whether they have
	// copies in other folders. Messages with copies only need label removal
	// (DELETE without COPY). Messages without copies need a real move (COPY + DELETE).
	var moveUIDs, labelRemovalUIDs []goImap.UID
	if isGmail && destIsTrashOrSpam {
		for _, m := range messages {
			hasCopies := false
			if m.MessageID != "" {
				var copyErr error
				hasCopies, copyErr = a.messageStore.HasCopiesInOtherFolders(m.MessageID, sourceFolderID, accountID)
				if copyErr != nil {
					log.Warn().Err(copyErr).Str("messageID", m.MessageID).Msg("Failed to check for copies, treating as sole copy")
				}
			}
			if hasCopies {
				labelRemovalUIDs = append(labelRemovalUIDs, goImap.UID(m.UID))
				continue
			}
			moveUIDs = append(moveUIDs, goImap.UID(m.UID))
		}
		log.Info().
			Int("moveCount", len(moveUIDs)).
			Int("labelRemovalCount", len(labelRemovalUIDs)).
			Msg("Gmail: partitioned messages for trash/spam operation")
	}
	if !isGmail || !destIsTrashOrSpam {
		for _, m := range messages {
			moveUIDs = append(moveUIDs, goImap.UID(m.UID))
		}
	}

	// Combine all UIDs that need DELETE from source
	allUIDs := append(moveUIDs, labelRemovalUIDs...)
	if len(allUIDs) == 0 {
		return nil
	}

	// Mark the IDLE echo-suppression window at start AND completion so the
	// EXISTS/EXPUNGE echoes of this op defer the inbox reconcile instead of
	// racing our own in-flight EXPUNGE (see ownExpungeEchoSuppress).
	a.noteOwnExpunge(accountID)
	defer a.noteOwnExpunge(accountID)

	err = a.withIMAPRetry(accountID, func(conn *imap.Client) error {
		// Select source mailbox
		log.Debug().Str("mailbox", sourceFolder.Path).Msg("Selecting source mailbox")
		if _, err := conn.SelectMailbox(a.ctx, sourceFolder.Path); err != nil {
			return fmt.Errorf("failed to select source mailbox: %w", err)
		}

		// COPY only the messages that need a real move (not label-removal-only)
		if len(moveUIDs) > 0 {
			log.Debug().Str("destMailbox", destFolder.Path).Int("count", len(moveUIDs)).Msg("Copying messages to destination")
			if _, err := conn.CopyMessages(moveUIDs, destFolder.Path); err != nil {
				return fmt.Errorf("failed to copy messages: %w", err)
			}
			log.Debug().Msg("Messages copied successfully")
		}

		// DELETE all UIDs from source (both moved and label-removed)
		log.Debug().Int("count", len(allUIDs)).Msg("Deleting messages from source (marking deleted + expunge)")
		if err := conn.DeleteMessagesByUID(allUIDs); err != nil {
			return fmt.Errorf("failed to delete messages from source: %w", err)
		}

		return nil
	})

	if err != nil {
		log.Error().Err(err).Msg("IMAP move operation failed")
		return err
	}

	log.Info().
		Str("sourceFolder", sourceFolder.Path).
		Str("destFolder", destFolder.Path).
		Int("count", len(messages)).
		Msg("IMAP move operation completed successfully")

	return nil
}

// CopyToFolder copies messages to a specified folder (keeps original)
// Unlike MoveToFolder, this only copies - original messages remain in place
func (a *App) CopyToFolder(messageIDs []string, destFolderID string) error {
	log := logging.WithComponent("app")

	if len(messageIDs) == 0 {
		return nil
	}

	// Cross-account selections route to a partition helper that recurses
	// through this same function with uniform single-account slices. See
	// MoveToFolder for the rationale — same shape applied to Copy (which
	// keeps originals in place rather than deleting them).
	if spans, _ := a.messageStore.SpansMultipleAccounts(messageIDs); spans {
		return a.copyToFolderCrossAccount(messageIDs, destFolderID)
	}

	messages, err := a.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}

	destFolder, err := a.folderStore.Get(destFolderID)
	if err != nil || destFolder == nil {
		return fmt.Errorf("destination folder not found: %s", destFolderID)
	}

	// Cross-account copy: APPEND raw bytes to destination. Fire-and-forget to
	// match intra-account CopyToFolder's existing async behavior.
	if messages[0].AccountID != destFolder.AccountID {
		go func() {
			defer recoverPanic("app.actions", "cross-account copy")
			if err := a.copyMessagesAcrossAccounts(messages, destFolder); err != nil {
				log.Error().Err(err).
					Str("sourceAccountID", messages[0].AccountID).
					Str("destAccountID", destFolder.AccountID).
					Str("destFolderID", destFolder.ID).
					Msg("Cross-account copy failed")
				return
			}
			// Dest folder refresh + sidebar count bump ride on folder:synced from SyncFolder.
			_ = a.SyncFolder(destFolder.AccountID, destFolder.ID)
		}()
		return nil
	}

	// Group by source folder
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	// Copy on IMAP (no local DB change - messages stay in source folder)
	go func() {
		defer recoverPanic("app.actions", "copy messages on IMAP")
		for sourceFolderID, msgs := range byFolder {
			if err := a.copyMessagesToIMAP(msgs, sourceFolderID, destFolder); err != nil {
				log.Error().Err(err).
					Str("sourceFolderID", sourceFolderID).
					Str("destFolderID", destFolderID).
					Msg("Failed to copy messages on IMAP")
			}
		}

		// Sync destination folder so copied messages appear (headers + bodies)
		// Clear debounce so this request isn't silently dropped
		if len(messages) > 0 {
			accountID := messages[0].AccountID
			syncKey := accountID + ":" + destFolderID
			a.syncMu.Lock()
			delete(a.syncLastRequest, syncKey)
			a.syncMu.Unlock()

			if err := a.SyncFolder(accountID, destFolderID); err != nil && err != context.Canceled {
				log.Warn().Err(err).Str("destFolderID", destFolderID).Msg("Failed to sync destination folder after copy")
			}
		}
		// Dest folder refresh + sidebar count bump ride on folder:synced from SyncFolder above.
	}()

	return nil
}

func (a *App) copyMessagesToIMAP(messages []*message.Message, sourceFolderID string, destFolder *folder.Folder) error {
	if len(messages) == 0 {
		return nil
	}

	sourceFolder, err := a.folderStore.Get(sourceFolderID)
	if err != nil || sourceFolder == nil {
		return fmt.Errorf("source folder not found")
	}

	uids := make([]goImap.UID, len(messages))
	for i, m := range messages {
		uids[i] = goImap.UID(m.UID)
	}

	return a.withIMAPRetry(messages[0].AccountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(a.ctx, sourceFolder.Path); err != nil {
			return fmt.Errorf("failed to select source mailbox: %w", err)
		}

		// COPY to destination (no DELETE - messages stay in source)
		if _, err := conn.CopyMessages(uids, destFolder.Path); err != nil {
			return fmt.Errorf("failed to copy messages: %w", err)
		}

		return nil
	})
}

// flagsForAppend maps a local message's flag fields to the IMAP flag slice
// used by AppendMessage on cross-account copy/move.
func flagsForAppend(m *message.Message) []goImap.Flag {
	flags := []goImap.Flag{}
	if m.IsRead {
		flags = append(flags, goImap.FlagSeen)
	}
	if m.IsStarred {
		flags = append(flags, goImap.FlagFlagged)
	}
	if m.IsAnswered {
		flags = append(flags, goImap.FlagAnswered)
	}
	if m.IsDraft {
		flags = append(flags, goImap.FlagDraft)
	}
	return flags
}

// copyMessagesAcrossAccounts fetches each message's raw RFC822 bytes from the
// source account's IMAP and APPENDs them to the destination account's IMAP.
// Used by the cross-account branches of MoveToFolder and CopyToFolder.
//
// Synchronous: returns when every APPEND has succeeded. Caller is expected to
// short-circuit on error so the source side is never touched after a partial
// success/failure.
func (a *App) copyMessagesAcrossAccounts(messages []*message.Message, destFolder *folder.Folder) error {
	log := logging.WithComponent("app.copyMessagesAcrossAccounts")

	if len(messages) == 0 {
		return nil
	}

	log.Info().
		Str("sourceAccountID", messages[0].AccountID).
		Str("destAccountID", destFolder.AccountID).
		Str("destFolder", destFolder.Path).
		Int("count", len(messages)).
		Msg("Starting cross-account append")

	return a.withIMAPRetry(destFolder.AccountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(a.ctx, destFolder.Path); err != nil {
			return fmt.Errorf("failed to select destination mailbox: %w", err)
		}
		for _, m := range messages {
			raw, err := a.syncEngine.FetchRawMessage(a.ctx, m.AccountID, m.FolderID, m.UID)
			if err != nil {
				return fmt.Errorf("failed to fetch raw message from source: %w", err)
			}
			if _, err := conn.AppendMessage(destFolder.Path, flagsForAppend(m), m.Date, raw); err != nil {
				return fmt.Errorf("failed to append to destination: %w", err)
			}
		}
		log.Info().
			Str("destFolder", destFolder.Path).
			Int("count", len(messages)).
			Msg("Cross-account append completed")
		return nil
	})
}

// Archive moves messages to the Archive folder. One call is one undoable
// action — see pushUndoCommands.
func (a *App) Archive(messageIDs []string) error {
	cmds, destName, err := a.archiveCmds(messageIDs)
	a.pushUndoCommands(destName, cmds)
	return err
}

func (a *App) archiveCmds(messageIDs []string) ([]undo.Command, string, error) {
	if len(messageIDs) == 0 {
		return nil, "", nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	// Cross-account selections (Unified Inbox) route to a partition
	// helper that recurses through this same function with uniform
	// single-account slices. SpansMultipleAccounts errors fall through
	// to the fast path so a metadata hiccup never breaks what works.
	if spans, _ := a.messageStore.SpansMultipleAccounts(messageIDs); spans {
		return a.archiveCrossAccountCmds(messageIDs)
	}

	// Get first message to determine account
	messages, err := a.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return nil, "", fmt.Errorf("failed to get message")
	}

	archiveFolder, err := a.GetSpecialFolder(messages[0].AccountID, folder.TypeArchive)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get archive folder: %w", err)
	}
	if archiveFolder == nil {
		return nil, "", fmt.Errorf("no archive folder configured")
	}

	return a.moveToFolderCmds(messageIDs, archiveFolder.ID)
}

// Trash moves messages to the Trash folder.
// Returns true if at least one message was moved to trash (show undo toast).
// Returns false if all messages were just label-removed on Gmail (no undo).
// One call is one undoable action — see pushUndoCommands.
func (a *App) Trash(messageIDs []string) (bool, error) {
	moved, cmds, destName, err := a.trashCmds(messageIDs)
	a.pushUndoCommands(destName, cmds)
	return moved, err
}

func (a *App) trashCmds(messageIDs []string) (bool, []undo.Command, string, error) {
	if len(messageIDs) == 0 {
		return false, nil, "", nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	// Cross-account selections (Unified Inbox) route to a partition
	// helper that recurses through this same function with uniform
	// single-account slices. SpansMultipleAccounts errors fall through
	// to the fast path so a metadata hiccup never breaks what works.
	if spans, _ := a.messageStore.SpansMultipleAccounts(messageIDs); spans {
		return a.trashCrossAccountCmds(messageIDs)
	}

	messages, err := a.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return false, nil, "", fmt.Errorf("failed to get message")
	}

	accountID := messages[0].AccountID

	trashFolder, err := a.GetSpecialFolder(accountID, folder.TypeTrash)
	if err != nil {
		return false, nil, "", fmt.Errorf("failed to get trash folder: %w", err)
	}
	if trashFolder == nil {
		return false, nil, "", fmt.Errorf("no trash folder configured")
	}

	// Non-Gmail: normal move to trash for all messages
	if !a.isGmailAccount(accountID) {
		cmds, _, err := a.moveToFolderCmds(messageIDs, trashFolder.ID)
		return true, cmds, trashFolder.Name, err
	}

	// Gmail: partition messages into copies (label-remove) vs sole copies (move to trash)
	moved, cmds, err := a.gmailTrashOrSpamCmds(messageIDs, trashFolder)
	return moved, cmds, trashFolder.Name, err
}

// gmailTrashOrSpamCmds handles Gmail-specific trash/spam behavior.
// Messages with copies in other folders get label-removed (DELETE only).
// Messages without copies get moved to the destination folder (COPY + DELETE).
// Returns true if at least one message was moved to dest (show undo toast),
// plus the undo commands the move created (label-removed copies create
// none — they have no undo entry, see the Trash doc comment).
func (a *App) gmailTrashOrSpamCmds(messageIDs []string, destFolder *folder.Folder) (bool, []undo.Command, error) {
	log := logging.WithComponent("app.gmailTrashOrSpam")

	allMessages, err := a.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get messages: %w", err)
	}
	if len(allMessages) == 0 {
		return false, nil, nil
	}

	accountID := allMessages[0].AccountID

	// Partition: copies (exist in other folders) vs non-copies (sole copy)
	var copyIDs, nonCopyIDs []string
	var copyMsgs []*message.Message
	for _, m := range allMessages {
		hasCopies := false
		if m.MessageID != "" {
			hasCopies, err = a.messageStore.HasCopiesInOtherFolders(m.MessageID, m.FolderID, accountID)
			if err != nil {
				log.Warn().Err(err).Str("messageID", m.MessageID).Msg("Failed to check for copies, treating as sole copy")
			}
		}
		if hasCopies {
			copyIDs = append(copyIDs, m.ID)
			copyMsgs = append(copyMsgs, m)
			continue
		}
		nonCopyIDs = append(nonCopyIDs, m.ID)
	}

	log.Info().
		Int("copyCount", len(copyIDs)).
		Int("nonCopyCount", len(nonCopyIDs)).
		Str("destFolder", destFolder.Name).
		Msg("Gmail: partitioned messages for trash/spam")

	// Handle copies: just remove the label (delete locally + IMAP DELETE without COPY)
	if len(copyIDs) > 0 {
		if err := a.gmailRemoveLabel(copyMsgs); err != nil {
			log.Error().Err(err).Msg("Failed to remove Gmail labels for copies")
			return len(nonCopyIDs) > 0, nil, err
		}
	}

	// Handle non-copies: normal move to trash/spam
	if len(nonCopyIDs) > 0 {
		cmds, _, err := a.moveToFolderCmds(nonCopyIDs, destFolder.ID)
		if err != nil {
			return true, cmds, err
		}
		return true, cmds, nil
	}

	return len(nonCopyIDs) > 0, nil, nil
}

// gmailRemoveLabel removes messages from their current folder (label) on Gmail.
// This deletes them locally and does IMAP DELETE from source without COPY —
// effectively removing the label while the message stays in other labels.
func (a *App) gmailRemoveLabel(messages []*message.Message) error {
	log := logging.WithComponent("app.gmailRemoveLabel")

	if len(messages) == 0 {
		return nil
	}

	// Group by source folder
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	// Collect all IDs for local delete
	ids := make([]string, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
	}

	// Stamp the own-expunge window BEFORE the local mutation (the IMAP
	// goroutine's stamp runs later — see MoveToFolder)
	a.noteOwnExpunge(messages[0].AccountID)

	// Delete from local DB
	if err := a.messageStore.DeleteBatch(ids); err != nil {
		return fmt.Errorf("failed to delete messages locally: %w", err)
	}

	wailsRuntime.EventsEmit(a.ctx, "messages:deleted", ids)

	// Update folder counts (deltas — see folder_counts.go)
	go func() {
		defer recoverPanic("app.actions", "update folder counts after label removal")
		folderCounts := make(map[string]int)
		for folderID, msgs := range byFolder {
			a.adjustFolderCounts(folderCounts, folderID, -len(msgs), -unreadIn(msgs))
		}
		a.emitFolderCounts(folderCounts)
	}()

	// IMAP: DELETE from source folders (no COPY — just remove the label)
	go func() {
		defer recoverPanic("app.actions", "remove Gmail label on IMAP")
		for folderID, msgs := range byFolder {
			if err := a.removeFromIMAPFolder(msgs, folderID); err != nil {
				log.Error().Err(err).Str("folderID", folderID).Msg("Failed to remove messages from IMAP folder")
			}
		}
	}()

	return nil
}

// removeFromIMAPFolder does SELECT + DELETE by UID on the given folder.
// Unlike deleteMessagesFromIMAP, this does NOT check HasCopiesInOtherFolders —
// it unconditionally removes the messages from the folder (removes the Gmail label).
func (a *App) removeFromIMAPFolder(messages []*message.Message, folderID string) error {
	if len(messages) == 0 {
		return nil
	}

	folderObj, err := a.folderStore.Get(folderID)
	if err != nil || folderObj == nil {
		return fmt.Errorf("folder not found")
	}

	var uids []goImap.UID
	for _, m := range messages {
		if m.UID == 0 || int32(m.UID) < 0 {
			continue
		}
		uids = append(uids, goImap.UID(m.UID))
	}
	if len(uids) == 0 {
		return nil
	}

	// See ownExpungeEchoSuppress — defer the IDLE echo of our own delete.
	a.noteOwnExpunge(messages[0].AccountID)
	defer a.noteOwnExpunge(messages[0].AccountID)

	return a.withIMAPRetry(messages[0].AccountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(a.ctx, folderObj.Path); err != nil {
			return fmt.Errorf("failed to select mailbox: %w", err)
		}
		return conn.DeleteMessagesByUID(uids)
	})
}

// MarkAsSpam moves messages to the Spam folder.
// Returns true if at least one message was moved to spam (show undo toast).
// Returns false if all messages were just label-removed on Gmail (no undo).
func (a *App) MarkAsSpam(messageIDs []string) (bool, error) {
	moved, cmds, destName, err := a.markAsSpamCmds(messageIDs)
	a.pushUndoCommands(destName, cmds)
	return moved, err
}

func (a *App) markAsSpamCmds(messageIDs []string) (bool, []undo.Command, string, error) {
	if len(messageIDs) == 0 {
		return false, nil, "", nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	// Cross-account selections (Unified Inbox) route to a partition
	// helper that recurses through this same function with uniform
	// single-account slices.
	if spans, _ := a.messageStore.SpansMultipleAccounts(messageIDs); spans {
		return a.markAsSpamCrossAccountCmds(messageIDs)
	}

	messages, err := a.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return false, nil, "", fmt.Errorf("failed to get message")
	}

	accountID := messages[0].AccountID

	spamFolder, err := a.GetSpecialFolder(accountID, folder.TypeSpam)
	if err != nil {
		return false, nil, "", fmt.Errorf("failed to get spam folder: %w", err)
	}
	if spamFolder == nil {
		return false, nil, "", fmt.Errorf("no spam folder configured")
	}

	// Non-Gmail: normal move to spam
	if !a.isGmailAccount(accountID) {
		cmds, _, err := a.moveToFolderCmds(messageIDs, spamFolder.ID)
		return true, cmds, spamFolder.Name, err
	}

	// Gmail: partition messages into copies (label-remove) vs sole copies (move to spam)
	moved, cmds, err := a.gmailTrashOrSpamCmds(messageIDs, spamFolder)
	return moved, cmds, spamFolder.Name, err
}

// MarkAsNotSpam moves messages from Spam to Inbox. One call is one
// undoable action — see pushUndoCommands.
func (a *App) MarkAsNotSpam(messageIDs []string) error {
	cmds, destName, err := a.markAsNotSpamCmds(messageIDs)
	a.pushUndoCommands(destName, cmds)
	return err
}

func (a *App) markAsNotSpamCmds(messageIDs []string) ([]undo.Command, string, error) {
	if len(messageIDs) == 0 {
		return nil, "", nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	// Cross-account selections (Unified Inbox) route to a partition
	// helper that recurses through this same function with uniform
	// single-account slices.
	if spans, _ := a.messageStore.SpansMultipleAccounts(messageIDs); spans {
		return a.markAsNotSpamCrossAccountCmds(messageIDs)
	}

	messages, err := a.messageStore.GetByIDs(messageIDs[:1])
	if err != nil || len(messages) == 0 {
		return nil, "", fmt.Errorf("failed to get message")
	}

	inboxFolder, err := a.folderStore.GetByType(messages[0].AccountID, folder.TypeInbox)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get inbox folder: %w", err)
	}
	if inboxFolder == nil {
		return nil, "", fmt.Errorf("no inbox folder found")
	}

	return a.moveToFolderCmds(messageIDs, inboxFolder.ID)
}

// validateEmptyFolder guards the destructive empty-folder operations. The
// folder must exist, belong to the given account, and be of the expected
// type — otherwise a wrong or stale folderID from any caller would
// permanently delete an arbitrary folder's contents, including Inbox.
// Checked before any message IDs are even listed.
func (a *App) validateEmptyFolder(accountID, folderID string, want folder.Type) error {
	f, err := a.folderStore.Get(folderID)
	if err != nil {
		return fmt.Errorf("failed to get folder: %w", err)
	}
	if f == nil {
		return fmt.Errorf("folder not found: %s", folderID)
	}
	if f.AccountID != accountID {
		return fmt.Errorf("folder %s does not belong to account %s", folderID, accountID)
	}
	if f.Type != want {
		return fmt.Errorf("folder %s is %q, not %q — refusing to empty it", folderID, f.Type, want)
	}
	return nil
}

// EmptyTrash permanently deletes all messages in a trash folder
func (a *App) EmptyTrash(accountID, folderID string) error {
	if err := a.validateEmptyFolder(accountID, folderID, folder.TypeTrash); err != nil {
		return err
	}
	ids, err := a.messageStore.GetAllIDsByFolder(folderID)
	if err != nil {
		return fmt.Errorf("failed to get messages in trash: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	return a.DeletePermanently(ids)
}

// EmptySpam permanently deletes all messages in a spam folder. Same
// folder-generic mechanics as EmptyTrash, with its own type guard: a spam
// folder must never be emptied through the trash path or vice versa.
func (a *App) EmptySpam(accountID, folderID string) error {
	if err := a.validateEmptyFolder(accountID, folderID, folder.TypeSpam); err != nil {
		return err
	}
	ids, err := a.messageStore.GetAllIDsByFolder(folderID)
	if err != nil {
		return fmt.Errorf("failed to get messages in spam: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	return a.DeletePermanently(ids)
}

// DeletePermanently permanently deletes messages
func (a *App) DeletePermanently(messageIDs []string) error {
	log := logging.WithComponent("app")

	if len(messageIDs) == 0 {
		return nil
	}
	messageIDs = a.expandToFolderCopies(messageIDs)

	messages, err := a.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}
	if len(messages) == 0 {
		return nil
	}

	// Group by folder
	byFolder := make(map[string][]*message.Message)
	for _, m := range messages {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m)
	}

	// Stamp the own-expunge window BEFORE the local mutation (the IMAP
	// goroutine's stamp runs later — see MoveToFolder)
	a.noteOwnExpunge(messages[0].AccountID)

	// Delete from local DB first
	if err := a.messageStore.DeleteBatch(messageIDs); err != nil {
		return fmt.Errorf("failed to delete messages locally: %w", err)
	}

	wailsRuntime.EventsEmit(a.ctx, "messages:deleted", messageIDs)

	// Update folder counts (deltas — see folder_counts.go)
	go func() {
		defer recoverPanic("app.actions", "update folder counts after delete")
		folderCounts := make(map[string]int)
		for folderID, msgs := range byFolder {
			a.adjustFolderCounts(folderCounts, folderID, -len(msgs), -unreadIn(msgs))
		}
		a.emitFolderCounts(folderCounts)
	}()

	// Delete from IMAP in background
	go func() {
		defer recoverPanic("app.actions", "delete from IMAP")
		for folderID, msgs := range byFolder {
			if err := a.deleteMessagesFromIMAP(msgs, folderID); err != nil {
				log.Error().Err(err).Str("folderID", folderID).Msg("Failed to delete messages from IMAP")
			}
		}
	}()

	// Note: Permanent delete undo is complex - would need to store full message content
	// For now, we don't add to undo stack for permanent deletes

	return nil
}

func (a *App) deleteMessagesFromIMAP(messages []*message.Message, folderID string) error {
	if len(messages) == 0 {
		return nil
	}

	log := logging.WithComponent("app.deleteMessagesFromIMAP")

	folderObj, err := a.folderStore.Get(folderID)
	if err != nil || folderObj == nil {
		return fmt.Errorf("folder not found")
	}

	accountID := messages[0].AccountID
	isGmail := a.isGmailAccount(accountID)

	var uids []goImap.UID
	for _, m := range messages {
		// Skip messages with temp UIDs (negative values from local move operations)
		// that haven't been reconciled with the IMAP server yet
		if m.UID == 0 || int32(m.UID) < 0 {
			continue
		}

		// For Gmail: skip IMAP delete if the same RFC 822 Message-ID exists in
		// other local folders. On Gmail there's only ONE underlying message — an
		// IMAP EXPUNGE here would destroy it across ALL labels (Inbox, etc.).
		if isGmail && m.MessageID != "" {
			hasCopies, copyErr := a.messageStore.HasCopiesInOtherFolders(m.MessageID, folderID, accountID)
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

	// See ownExpungeEchoSuppress — defer the IDLE echo of our own delete.
	a.noteOwnExpunge(accountID)
	defer a.noteOwnExpunge(accountID)

	return a.withIMAPRetry(accountID, func(conn *imap.Client) error {
		if _, err := conn.SelectMailbox(a.ctx, folderObj.Path); err != nil {
			return fmt.Errorf("failed to select mailbox: %w", err)
		}

		return conn.DeleteMessagesByUID(uids)
	})
}

// ============================================================================
// Cross-account dispatch helpers
//
// Bulk actions invoked from Unified Inbox may carry a selection that spans
// multiple accounts. The public entry points (Trash, Archive, MarkAsSpam,
// MarkAsNotSpam, MoveToFolder, CopyToFolder) detect this up front via
// messageStore.SpansMultipleAccounts and route here. Each helper partitions
// the IDs by AccountID, then loops by re-invoking the internal *Cmds
// variant with each partition's single-account slice, aggregating the undo
// commands. The public wrapper pushes the aggregate as ONE undoable action
// (see pushUndoCommands), so a single Undo reverses the whole bulk action.
// Recursion depth is bounded at 1 by construction (partition slices are
// uniform-account by definition).
// ============================================================================

// partitionByAccount groups message IDs by their owning account. Invalid
// IDs (no matching row in the store) are silently dropped — matching the
// behavior of every other call site that consumes GetByIDs results.
func (a *App) partitionByAccount(messageIDs []string) (map[string][]string, error) {
	msgs, err := a.messageStore.GetByIDs(messageIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	byAccount := make(map[string][]string)
	for _, m := range msgs {
		byAccount[m.AccountID] = append(byAccount[m.AccountID], m.ID)
	}
	return byAccount, nil
}

// trashCrossAccountCmds fan-outs trashCmds per account partition. Aggregates
// the (bool, error) returns: anyMoved is OR'd across partitions, firstErr wins.
// The undo commands from every partition are aggregated for the caller to
// push as one undoable action.
func (a *App) trashCrossAccountCmds(messageIDs []string) (bool, []undo.Command, string, error) {
	byAccount, err := a.partitionByAccount(messageIDs)
	if err != nil {
		return false, nil, "", err
	}
	var anyMoved bool
	var cmds []undo.Command
	var destName string
	var firstErr error
	for _, ids := range byAccount {
		moved, partCmds, partDest, err := a.trashCmds(ids)
		if moved {
			anyMoved = true
		}
		cmds = append(cmds, partCmds...)
		if destName == "" {
			destName = partDest
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return anyMoved, cmds, destName, firstErr
}

// archiveCrossAccountCmds fan-outs archiveCmds per account partition.
// Aggregates first error encountered, and the undo commands for the caller
// to push as one undoable action.
func (a *App) archiveCrossAccountCmds(messageIDs []string) ([]undo.Command, string, error) {
	byAccount, err := a.partitionByAccount(messageIDs)
	if err != nil {
		return nil, "", err
	}
	var cmds []undo.Command
	var destName string
	var firstErr error
	for _, ids := range byAccount {
		partCmds, partDest, err := a.archiveCmds(ids)
		cmds = append(cmds, partCmds...)
		if destName == "" {
			destName = partDest
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return cmds, destName, firstErr
}

// markAsSpamCrossAccountCmds fan-outs markAsSpamCmds per account partition.
func (a *App) markAsSpamCrossAccountCmds(messageIDs []string) (bool, []undo.Command, string, error) {
	byAccount, err := a.partitionByAccount(messageIDs)
	if err != nil {
		return false, nil, "", err
	}
	var anyMoved bool
	var cmds []undo.Command
	var destName string
	var firstErr error
	for _, ids := range byAccount {
		moved, partCmds, partDest, err := a.markAsSpamCmds(ids)
		if moved {
			anyMoved = true
		}
		cmds = append(cmds, partCmds...)
		if destName == "" {
			destName = partDest
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return anyMoved, cmds, destName, firstErr
}

// markAsNotSpamCrossAccountCmds fan-outs markAsNotSpamCmds per account partition.
func (a *App) markAsNotSpamCrossAccountCmds(messageIDs []string) ([]undo.Command, string, error) {
	byAccount, err := a.partitionByAccount(messageIDs)
	if err != nil {
		return nil, "", err
	}
	var cmds []undo.Command
	var destName string
	var firstErr error
	for _, ids := range byAccount {
		partCmds, partDest, err := a.markAsNotSpamCmds(ids)
		cmds = append(cmds, partCmds...)
		if destName == "" {
			destName = partDest
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return cmds, destName, firstErr
}

// moveToFolderCrossAccountCmds fan-outs moveToFolderCmds per source-account
// partition. Each recursive call hits the internal variant with a
// uniform-source-account slice; the existing
// `messages[0].AccountID != destFolder.AccountID` guard in that function
// then classifies the WHOLE partition correctly:
//   - partition's account == dest's account → same-account move path.
//   - partition's account != dest's account → existing cross-account
//     APPEND-then-Trash path (copyMessagesAcrossAccounts + recursive
//     trashCmds on the partition's IDs).
//
// Each partition's outcome is independent — a Gmail partition's failure
// doesn't block an IMAP partition's success. The undo commands from every
// partition are aggregated for the caller to push as one undoable action.
func (a *App) moveToFolderCrossAccountCmds(messageIDs []string, destFolderID string) ([]undo.Command, string, error) {
	byAccount, err := a.partitionByAccount(messageIDs)
	if err != nil {
		return nil, "", err
	}
	var cmds []undo.Command
	var destName string
	var firstErr error
	for _, ids := range byAccount {
		partCmds, partDest, err := a.moveToFolderCmds(ids, destFolderID)
		cmds = append(cmds, partCmds...)
		if destName == "" {
			destName = partDest
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return cmds, destName, firstErr
}

// copyToFolderCrossAccount fan-outs CopyToFolder() per source-account
// partition. Mirrors moveToFolderCrossAccount but Copy keeps originals.
func (a *App) copyToFolderCrossAccount(messageIDs []string, destFolderID string) error {
	byAccount, err := a.partitionByAccount(messageIDs)
	if err != nil {
		return err
	}
	var firstErr error
	for _, ids := range byAccount {
		if err := a.CopyToFolder(ids, destFolderID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
