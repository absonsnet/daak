package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/absonsnet/daak/internal/message"
)

// cleanupMovedTempUIDs finishes a local move once the IMAP COPY+DELETE has
// run: it runs the destination-folder sync (syncDest) and only then deletes
// the temp negative-UID rows MoveMessages left behind — and only for the
// moved messages whose real server row the sync actually fetched into the
// destination folder.
//
// The old code deleted the temp rows unconditionally after the sync call,
// so a failed destination sync lost the message's local state (read flag
// and friends) even though the server copy reappeared later as new; and
// the detached viewer never ran the step at all, so a move from a popup
// left the temp rows behind and the message appeared twice (re-audit
// finding R15). Both the main window (App.MoveToFolder, whose syncDest is
// App.SyncFolder) and the detached viewer (ViewerApp, whose syncDest is a
// direct syncEngine.SyncMessages call — the viewer has no App.SyncFolder
// wrapper, debounce, or sender rules, and its engine is constructed but
// never started) share this helper so the rule cannot drift apart again.
//
// It returns the number of moved messages whose temp rows were deleted.
// An error is returned when the sync failed, the confirmation lookup
// failed, or the delete failed; messages whose server copy was not
// confirmed keep their temp rows (the message stays visible in the
// destination folder with its local state, and a later move's sync
// retries the cleanup). A partial result — some messages confirmed, some
// not — still deletes the confirmed ones and reports the unconfirmed ones
// in the error.
func cleanupMovedTempUIDs(store *message.Store, destFolderID string, moved []*message.Message, syncDest func() error) (int, error) {
	if syncDest == nil {
		return 0, errors.New("no destination sync step, keeping temp rows after move")
	}
	if err := syncDest(); err != nil {
		return 0, fmt.Errorf("destination folder sync failed, keeping temp rows after move: %w", err)
	}

	var rfc822IDs []string
	seen := make(map[string]struct{})
	for _, m := range moved {
		if m != nil && m.MessageID != "" {
			if _, dup := seen[m.MessageID]; dup {
				continue
			}
			seen[m.MessageID] = struct{}{}
			rfc822IDs = append(rfc822IDs, m.MessageID)
		}
	}
	if len(rfc822IDs) == 0 {
		return 0, errors.New("moved messages carry no Message-ID, cannot confirm server copies; keeping temp rows after move")
	}

	confirmed, err := store.ConfirmedRealCopies(destFolderID, rfc822IDs)
	if err != nil {
		return 0, fmt.Errorf("failed to confirm server copies in destination folder, keeping temp rows after move: %w", err)
	}
	if len(confirmed) == 0 {
		return 0, fmt.Errorf("no server copies confirmed in destination folder, keeping temp rows after move")
	}

	var confirmedIDs []string
	var unconfirmed []string
	for _, mid := range rfc822IDs {
		if confirmed[normalizeMessageIDForCleanup(mid)] {
			confirmedIDs = append(confirmedIDs, mid)
		} else {
			unconfirmed = append(unconfirmed, mid)
		}
	}
	if err := store.DeleteTempUIDsForMessageIDs(destFolderID, confirmedIDs); err != nil {
		return 0, fmt.Errorf("failed to delete temp rows after move: %w", err)
	}
	if len(unconfirmed) > 0 {
		return len(confirmedIDs), fmt.Errorf("deleted temp rows for %d of %d moved messages; server copies not confirmed for %v, temp rows kept", len(confirmedIDs), len(rfc822IDs), unconfirmed)
	}
	return len(confirmedIDs), nil
}

// normalizeMessageIDForCleanup strips surrounding whitespace and angle
// brackets around a Message-ID, matching the keys ConfirmedRealCopies
// returns (and the store's normalizeMessageID).
func normalizeMessageIDForCleanup(mid string) string {
	mid = strings.TrimSpace(mid)
	return strings.Trim(mid, "<>")
}
