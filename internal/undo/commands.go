package undo

import (
	"context"
	"fmt"
	"strings"

	imapPkg "github.com/absonsnet/daak/internal/imap"
	"github.com/emersion/go-imap/v2"
)

// UndoContext provides dependencies for undo operations
type UndoContext interface {
	// GetIMAPConnectionForUndo returns an IMAP client for the account
	GetIMAPConnectionForUndo(ctx context.Context, accountID string) (*imapPkg.Client, func(), error)
	// UpdateLocalFlags updates flags in local database
	UpdateLocalFlags(messageIDs []string, isRead, isStarred *bool) error
	// MoveLocalMessages moves messages in local database
	MoveLocalMessages(messageIDs []string, folderID string) error
	// DeleteLocalMessages deletes messages from local database
	DeleteLocalMessages(messageIDs []string) error
	// FindLocalMessageIDs finds current local DB message IDs by RFC822 Message-ID and folder
	FindLocalMessageIDs(accountID, folderID string, rfc822MessageIDs []string) ([]string, error)
	// MoveMessagesToFolder moves messages using the full move pipeline (IMAP + local DB)
	MoveMessagesToFolder(messageIDs []string, destFolderID string) error
	// DeleteMessagesFromServer deletes messages from the server for
	// their account, honouring provider quirks (e.g. Gmail's
	// label-vs-folder behaviour and its copy guard). messageIDs are
	// current local DB IDs; the implementation resolves each
	// message's account, folder and UID itself.
	DeleteMessagesFromServer(messageIDs []string) error
	// ResolveMessageUID returns the message's CURRENT uid and folder ID
	// (looked up at undo time, so a move that reassigned the UID is
	// handled). UIDs <= 0 are temporary local placeholders with no
	// server counterpart yet.
	ResolveMessageUID(messageID string) (uint32, string, error)
}

// FlagChangeCommand handles read/star flag changes
type FlagChangeCommand struct {
	BaseCommand
	ctx           context.Context
	undoCtx       UndoContext
	accountID     string
	folderPath    string
	messageIDs    []string
	uids          []uint32
	flagType      string // "read" or "starred"
	previousState bool   // What was the state before
}

// NewFlagChangeCommand creates a new FlagChangeCommand
func NewFlagChangeCommand(
	ctx context.Context,
	undoCtx UndoContext,
	accountID, folderPath string,
	messageIDs []string,
	uids []uint32,
	flagType string,
	previousState bool,
	description string,
) *FlagChangeCommand {
	return &FlagChangeCommand{
		BaseCommand:   NewBaseCommand(description),
		ctx:           ctx,
		undoCtx:       undoCtx,
		accountID:     accountID,
		folderPath:    folderPath,
		messageIDs:    messageIDs,
		uids:          uids,
		flagType:      flagType,
		previousState: previousState,
	}
}

// Execute performs the action (already done at creation time)
func (c *FlagChangeCommand) Execute() error { return nil }

// Undo reverses the flag change
func (c *FlagChangeCommand) Undo() error {
	// Get IMAP connection
	client, release, err := c.undoCtx.GetIMAPConnectionForUndo(c.ctx, c.accountID)
	if err != nil {
		return fmt.Errorf("failed to get IMAP connection: %w", err)
	}
	defer release()

	// Select mailbox
	if _, err := client.SelectMailbox(c.ctx, c.folderPath); err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	// Convert UIDs
	imapUIDs := make([]imap.UID, len(c.uids))
	for i, uid := range c.uids {
		imapUIDs[i] = imap.UID(uid)
	}

	// Determine flag
	var flag imap.Flag
	switch c.flagType {
	case "read":
		flag = imap.FlagSeen
	case "starred":
		flag = imap.FlagFlagged
	default:
		return fmt.Errorf("unknown flag type: %s", c.flagType)
	}

	// Restore previous state on IMAP
	if c.previousState {
		if err := client.AddMessageFlags(imapUIDs, []imap.Flag{flag}); err != nil {
			return fmt.Errorf("failed to add flags: %w", err)
		}
	} else {
		if err := client.RemoveMessageFlags(imapUIDs, []imap.Flag{flag}); err != nil {
			return fmt.Errorf("failed to remove flags: %w", err)
		}
	}

	// Update local database
	var isRead, isStarred *bool
	switch c.flagType {
	case "read":
		isRead = &c.previousState
	case "starred":
		isStarred = &c.previousState
	}
	if err := c.undoCtx.UpdateLocalFlags(c.messageIDs, isRead, isStarred); err != nil {
		return fmt.Errorf("failed to update local flags: %w", err)
	}

	return nil
}

// MoveCommand handles moving messages between folders
type MoveCommand struct {
	BaseCommand
	undoCtx          UndoContext
	accountID        string
	rfc822MessageIDs []string // RFC822 Message-ID headers for reliable lookup
	sourceFolderID   string
	destFolderID     string
}

// NewMoveCommand creates a new MoveCommand
func NewMoveCommand(
	undoCtx UndoContext,
	accountID string,
	rfc822MessageIDs []string,
	sourceFolderID string,
	destFolderID string,
	description string,
) *MoveCommand {
	return &MoveCommand{
		BaseCommand:      NewBaseCommand(description),
		undoCtx:          undoCtx,
		accountID:        accountID,
		rfc822MessageIDs: rfc822MessageIDs,
		sourceFolderID:   sourceFolderID,
		destFolderID:     destFolderID,
	}
}

// Execute performs the action (already done at creation time)
func (c *MoveCommand) Execute() error { return nil }

// Undo reverses the move by finding current messages in the destination folder
// and moving them back using the standard move pipeline.
func (c *MoveCommand) Undo() error {
	// Find current local message IDs by RFC822 Message-ID in the destination folder
	localMsgIDs, err := c.undoCtx.FindLocalMessageIDs(c.accountID, c.destFolderID, c.rfc822MessageIDs)
	if err != nil {
		return fmt.Errorf("failed to find messages: %w", err)
	}
	if len(localMsgIDs) == 0 {
		return fmt.Errorf("messages not found in destination folder")
	}

	// Reuse the full move pipeline (IMAP + local DB + events)
	return c.undoCtx.MoveMessagesToFolder(localMsgIDs, c.sourceFolderID)
}

// CrossAccountMoveCommand undoes a move between two accounts. A
// cross-account move is copy-then-trash: APPEND the raw bytes to the
// destination account, then trash the source copy. Undoing only the
// trash leg (what the old code did) restored the source while leaving
// the destination copy in place — the message ended up in both
// accounts. This command records the destination copy and deletes it
// on Undo, so the move is fully reversed.
//
// Order matters: the trash leg is undone FIRST (restoring the source),
// then the destination copy is deleted. If the destination delete
// fails, the message survives in both accounts (the original bug,
// reported as an error) — the alternative order would delete the
// destination first and risk losing the message entirely when the
// trash leg then fails.
//
// Gmail-source wrinkle: the trash leg may have no undo command for
// copies that were only label-removed (they were never moved to
// Trash). Undo still deletes the destination copy; the source copy
// simply remains in its other folders, which is correct.
type CrossAccountMoveCommand struct {
	BaseCommand
	undoCtx          UndoContext
	destAccountID    string
	destFolderID     string
	rfc822MessageIDs []string // RFC822 Message-ID headers for reliable lookup
	trashCmds        []Command
}

// NewCrossAccountMoveCommand creates a new CrossAccountMoveCommand
func NewCrossAccountMoveCommand(
	undoCtx UndoContext,
	destAccountID, destFolderID string,
	rfc822MessageIDs []string,
	trashCmds []Command,
	description string,
) *CrossAccountMoveCommand {
	return &CrossAccountMoveCommand{
		BaseCommand:      NewBaseCommand(description),
		undoCtx:          undoCtx,
		destAccountID:    destAccountID,
		destFolderID:     destFolderID,
		rfc822MessageIDs: rfc822MessageIDs,
		trashCmds:        trashCmds,
	}
}

// Execute performs the action (already done at creation time)
func (c *CrossAccountMoveCommand) Execute() error { return nil }

// Undo restores the source copy via the trash leg, then deletes the
// destination copy (server first, local rows only once the server
// delete succeeded). Every leg is attempted even when an earlier one
// fails; the returned error names each failed leg.
func (c *CrossAccountMoveCommand) Undo() error {
	var errs []error

	// 1. Restore the source copy.
	for _, cmd := range c.trashCmds {
		if err := cmd.Undo(); err != nil {
			errs = append(errs, fmt.Errorf("trash leg: %w", err))
		}
	}

	// 2. Delete the destination copy.
	localMsgIDs, err := c.undoCtx.FindLocalMessageIDs(c.destAccountID, c.destFolderID, c.rfc822MessageIDs)
	if err != nil {
		errs = append(errs, fmt.Errorf("destination copy: failed to find messages: %w", err))
	} else if len(localMsgIDs) == 0 {
		errs = append(errs, fmt.Errorf("destination copy: messages not found in destination folder (the destination sync may not have run yet)"))
	} else {
		if err := c.undoCtx.DeleteMessagesFromServer(localMsgIDs); err != nil {
			errs = append(errs, fmt.Errorf("destination copy: failed to delete from server: %w", err))
		} else if err := c.undoCtx.DeleteLocalMessages(localMsgIDs); err != nil {
			errs = append(errs, fmt.Errorf("destination copy: failed to delete locally: %w", err))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	// The combined message names every failed leg.
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return fmt.Errorf("cross-account move undo partially failed: %s", strings.Join(msgs, "; "))
}
