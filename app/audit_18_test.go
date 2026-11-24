package app

import (
	"strings"
	"testing"

	"github.com/absonsnet/daak/internal/smtp"
)

// Audit finding 18 (v0.7.38 batch): the Undo toast must reliably
// appear. An IPC failure that would silently swallow the countdown
// toast now surfaces as an error instead.

// notifyMessageQueued with no IPC connection is an error, not a
// silent return: the detached composer keeps the window open and
// tells the user Undo lives in the Outbox.
func TestNotifyMessageQueuedWithoutIPC(t *testing.T) {
	c := &ComposerApp{}
	err := c.notifyMessageQueued("acc-1",
		smtp.ComposeMessage{Subject: "hello", To: []smtp.Address{{Address: "ada@example.com"}}},
		QueueSendResult{QueueID: "q-1", DraftID: "d-1", DelaySeconds: 5})
	if err == nil {
		t.Fatal("notifyMessageQueued with nil ipcClient returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "IPC") {
		t.Fatalf("error = %q, want it to mention the IPC connection", err.Error())
	}
}

// Same for the detached viewer's reaction notification.
func TestNotifyReactionQueuedWithoutIPC(t *testing.T) {
	v := &ViewerApp{}
	err := v.notifyReactionQueued("acc-1", "Re: hello", "ada@example.com",
		QueueSendResult{QueueID: "q-1", DelaySeconds: 5})
	if err == nil {
		t.Fatal("notifyReactionQueued with nil ipcClient returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "IPC") {
		t.Fatalf("error = %q, want it to mention the IPC connection", err.Error())
	}
}
