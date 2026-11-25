package sync

import "testing"

// Fourth-audit finding 2: a folder the server briefly fails to list
// must not be deleted locally with all its messages. The guards mirror
// message sync's reconcileUIDs (empty listing / mass disappearance /
// unhealthy pass refuse deletion for that pass only).
func TestShouldDeleteFolders(t *testing.T) {
	cases := []struct {
		name         string
		local        int
		delete       int
		statusFailed int
		want         bool
	}{
		{"nothing missing", 10, 0, 0, true},
		{"one folder genuinely removed", 10, 1, 0, true},
		{"half the folders missing", 10, 5, 0, true},
		{"more than half missing is refused", 10, 6, 0, false},
		{"all folders missing is refused", 10, 10, 0, false},
		{"single local folder removed", 1, 1, 0, true},
		{"two folders, one missing", 2, 1, 0, true},
		{"three folders, two missing is refused", 3, 2, 0, false},
		{"any STATUS failure blocks deletion", 10, 1, 1, false},
		{"STATUS failure with nothing missing is fine", 10, 0, 2, true},
		{"no local folders", 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldDeleteFolders(tc.local, tc.delete, tc.statusFailed); got != tc.want {
				t.Errorf("shouldDeleteFolders(%d, %d, %d) = %v, want %v",
					tc.local, tc.delete, tc.statusFailed, got, tc.want)
			}
		})
	}
}
