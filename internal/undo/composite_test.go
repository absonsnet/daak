package undo

import (
	"context"
	"errors"
	"testing"

	imapPkg "github.com/absonsnet/daak/internal/imap"
)

// TestCompositeCommand_UndoesInReverse verifies the chip-action undo
// contract: one Undo press reverses every grouped step, last first, and a
// failing step does not block the rest.
func TestCompositeCommand_UndoesInReverse(t *testing.T) {
	var order []string
	mk := func(name string, fail bool) *mockCommand {
		return &mockCommand{
			BaseCommand: NewBaseCommand(name),
			undoFn: func() error {
				order = append(order, name)
				if fail {
					return errors.New("boom")
				}
				return nil
			},
		}
	}
	c := NewCompositeCommand("chip", mk("move", false), mk("flag", false))
	if err := c.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(order) != 2 || order[0] != "flag" || order[1] != "move" {
		t.Fatalf("undo order = %v, want [flag move]", order)
	}

	// A failing middle step still runs the rest and reports the error.
	order = nil
	c2 := NewCompositeCommand("chip", mk("move", false), mk("flag", true), mk("extra", false))
	if err := c2.Undo(); err == nil {
		t.Fatalf("Undo should report the failing step")
	}
	if len(order) != 3 || order[0] != "extra" || order[1] != "flag" || order[2] != "move" {
		t.Fatalf("undo order with failure = %v, want [extra flag move]", order)
	}
}

// fakeUndoContext is a minimal UndoContext for FlagRestoreCommand tests.
// ResolveMessageUID returns a temporary UID, so no IMAP is attempted and
// the local flag restore is the whole behaviour under test.
type fakeUndoContext struct {
	updatedIDs []string
	isRead     *bool
	isStarred  *bool
}

func (f *fakeUndoContext) GetIMAPConnectionForUndo(ctx context.Context, accountID string) (*imapPkg.Client, func(), error) {
	return nil, nil, errors.New("no imap in test")
}

func (f *fakeUndoContext) UpdateLocalFlags(messageIDs []string, isRead, isStarred *bool) error {
	f.updatedIDs = messageIDs
	f.isRead = isRead
	f.isStarred = isStarred
	return nil
}

func (f *fakeUndoContext) MoveLocalMessages(messageIDs []string, folderID string) error {
	return nil
}

func (f *fakeUndoContext) DeleteLocalMessages(messageIDs []string) error { return nil }

func (f *fakeUndoContext) FindLocalMessageIDs(accountID, folderID string, rfc822MessageIDs []string) ([]string, error) {
	return nil, nil
}

func (f *fakeUndoContext) MoveMessagesToFolder(messageIDs []string, destFolderID string) error {
	return nil
}

func (f *fakeUndoContext) DeleteMessagesFromServer(messageIDs []string) error { return nil }

func (f *fakeUndoContext) ResolveMessageUID(messageID string) (uint32, string, error) {
	return 0, "trash-folder", nil // temp UID: no server counterpart
}

// TestFlagRestoreCommand_RestoresLocalFlagWithoutIMAP verifies that a
// message still carrying a temporary UID (destination sync hasn't run)
// still gets its local unread flag restored.
func TestFlagRestoreCommand_RestoresLocalFlagWithoutIMAP(t *testing.T) {
	fake := &fakeUndoContext{}
	cmd := NewFlagRestoreCommand(context.Background(), fake, "acct-1", "Trash",
		[]string{"msg-1"}, "read", false, "Mark as unread")
	if err := cmd.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(fake.updatedIDs) != 1 || fake.updatedIDs[0] != "msg-1" {
		t.Fatalf("updatedIDs = %v, want [msg-1]", fake.updatedIDs)
	}
	if fake.isRead == nil || *fake.isRead {
		t.Fatalf("isRead = %v, want false (unread)", fake.isRead)
	}
}
