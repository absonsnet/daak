package undo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	imapPkg "github.com/absonsnet/daak/internal/imap"
)

// recordingContext records the calls CrossAccountMoveCommand makes, so
// the test can assert the order and completeness of the undo legs.
type recordingContext struct {
	calls []string

	findIDs   []string // FindLocalMessageIDs returns these
	findErr   error
	serverErr error
	localErr  error
}

func (f *recordingContext) GetIMAPConnectionForUndo(context.Context, string) (*imapPkg.Client, func(), error) {
	return nil, nil, errors.New("no imap in test")
}
func (f *recordingContext) UpdateLocalFlags([]string, *bool, *bool) error { return nil }
func (f *recordingContext) MoveLocalMessages([]string, string) error      { return nil }
func (f *recordingContext) DeleteLocalMessages(ids []string) error {
	f.calls = append(f.calls, "delete-local:"+strings.Join(ids, ","))
	return f.localErr
}
func (f *recordingContext) FindLocalMessageIDs(accountID, folderID string, rfc822 []string) ([]string, error) {
	f.calls = append(f.calls, "find:"+accountID+"/"+folderID)
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findIDs, nil
}
func (f *recordingContext) MoveMessagesToFolder([]string, string) error { return nil }
func (f *recordingContext) DeleteMessagesFromServer(ids []string) error {
	f.calls = append(f.calls, "delete-server:"+strings.Join(ids, ","))
	return f.serverErr
}
func (f *recordingContext) ResolveMessageUID(string) (uint32, string, error) {
	return 0, "", nil
}

// fakeTrashCommand is one trash-leg undo command; it records whether
// its Undo ran and can be made to fail.
type fakeTrashCommand struct {
	undone bool
	err    error
}

func (c *fakeTrashCommand) Execute() error { return nil }
func (c *fakeTrashCommand) Undo() error {
	c.undone = true
	return c.err
}
func (c *fakeTrashCommand) Description() string  { return "trash leg" }
func (c *fakeTrashCommand) CreatedAt() time.Time { return time.Now() }

func newCrossAccountCmd(ctx *recordingContext, trash []Command) *CrossAccountMoveCommand {
	return NewCrossAccountMoveCommand(ctx, "dest-acct", "dest-folder",
		[]string{"<msg1@example.com>"}, trash, "Move to Dest")
}

// TestCrossAccountMoveCommand_UndoDeletesDestinationCopy (fourth-audit
// finding 12): Undo restores the source via the trash leg AND deletes
// the destination copy — server first, local rows only after the
// server delete succeeded — so the message ends up in exactly one
// account.
func TestCrossAccountMoveCommand_UndoDeletesDestinationCopy(t *testing.T) {
	ctx := &recordingContext{findIDs: []string{"local-dest-1"}}
	trash := &fakeTrashCommand{}
	cmd := newCrossAccountCmd(ctx, []Command{trash})
	if err := cmd.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !trash.undone {
		t.Fatal("trash leg Undo never ran")
	}
	want := []string{"find:dest-acct/dest-folder", "delete-server:local-dest-1", "delete-local:local-dest-1"}
	if len(ctx.calls) != len(want) {
		t.Fatalf("calls %v, want %v", ctx.calls, want)
	}
	for i, w := range want {
		if ctx.calls[i] != w {
			t.Fatalf("calls %v, want %v", ctx.calls, want)
		}
	}
}

// TestCrossAccountMoveCommand_UndoDestinationNotFound: when the
// destination sync has not run yet, the copy has no local rows and
// cannot be deleted by UID — Undo still restores the source and
// reports the destination leg as failed instead of silently leaving
// the message in both accounts.
func TestCrossAccountMoveCommand_UndoDestinationNotFound(t *testing.T) {
	ctx := &recordingContext{findIDs: nil}
	trash := &fakeTrashCommand{}
	cmd := newCrossAccountCmd(ctx, []Command{trash})
	err := cmd.Undo()
	if err == nil {
		t.Fatal("Undo should fail when the destination copy cannot be found")
	}
	if !trash.undone {
		t.Fatal("trash leg Undo must still run when the destination copy is missing")
	}
	if !strings.Contains(err.Error(), "destination copy") {
		t.Fatalf("error %q should name the destination leg", err)
	}
}

// TestCrossAccountMoveCommand_UndoTrashFailureStillDeletesDestination:
// a failing trash leg must not skip the destination delete — every
// leg is attempted and the error names the failed one.
func TestCrossAccountMoveCommand_UndoTrashFailureStillDeletesDestination(t *testing.T) {
	ctx := &recordingContext{findIDs: []string{"local-dest-1"}}
	trash := &fakeTrashCommand{err: errors.New("trash move failed")}
	cmd := newCrossAccountCmd(ctx, []Command{trash})
	err := cmd.Undo()
	if err == nil {
		t.Fatal("Undo should fail when the trash leg fails")
	}
	if !strings.Contains(err.Error(), "trash leg") {
		t.Fatalf("error %q should name the trash leg", err)
	}
	sawServerDelete := false
	for _, c := range ctx.calls {
		if c == "delete-server:local-dest-1" {
			sawServerDelete = true
		}
	}
	if !sawServerDelete {
		t.Fatalf("destination delete never attempted after trash failure: %v", ctx.calls)
	}
}

// TestCrossAccountMoveCommand_UndoServerDeleteFailureKeepsLocalRows:
// if the server delete fails, the local rows must stay — deleting
// them would hide a message that is still on the server (and the
// next sync would resurrect it).
func TestCrossAccountMoveCommand_UndoServerDeleteFailureKeepsLocalRows(t *testing.T) {
	ctx := &recordingContext{findIDs: []string{"local-dest-1"}, serverErr: errors.New("imap down")}
	trash := &fakeTrashCommand{}
	cmd := newCrossAccountCmd(ctx, []Command{trash})
	err := cmd.Undo()
	if err == nil {
		t.Fatal("Undo should fail when the server delete fails")
	}
	for _, c := range ctx.calls {
		if strings.HasPrefix(c, "delete-local:") {
			t.Fatalf("local rows deleted despite server delete failure: %v", ctx.calls)
		}
	}
}
