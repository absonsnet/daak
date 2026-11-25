package app

import (
	"strings"
	"testing"
)

// TestSaveAllResultError (fourth-audit finding 45): a Save All where
// every file saved reports no error; any failure produces an error
// naming how many saved and which filenames failed — so the UI shows
// the failure toast instead of claiming all files were saved.
func TestSaveAllResultError(t *testing.T) {
	if err := saveAllResultError(3, 3, nil); err != nil {
		t.Errorf("all saved: err = %v, want nil", err)
	}
	if err := saveAllResultError(0, 0, nil); err != nil {
		t.Errorf("empty: err = %v, want nil", err)
	}

	err := saveAllResultError(2, 3, []string{"broken.pdf"})
	if err == nil {
		t.Fatal("one failure: err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("err = %q, want it to name the saved/total counts", err.Error())
	}
	if !strings.Contains(err.Error(), "broken.pdf") {
		t.Errorf("err = %q, want it to name the failed file", err.Error())
	}

	err = saveAllResultError(0, 2, []string{"a.txt", "b.txt"})
	if err == nil {
		t.Fatal("all failed: err = nil, want an error")
	}
	if !strings.Contains(err.Error(), "0 of 2") || !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "b.txt") {
		t.Errorf("err = %q, want counts and both failed files", err.Error())
	}
}
