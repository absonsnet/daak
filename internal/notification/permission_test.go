package notification

import "testing"

// Re-audit finding R28: a denied macOS notification permission used to
// fail silently forever. The status mapping is the hinge — a denied
// permission must be reported as denied, never as silently disabled,
// and an unmapped value must never be reported as denied.
func TestDarwinAuthorizationStatusToPermission(t *testing.T) {
	tests := []struct {
		name string
		code int
		want PermissionStatus
	}{
		{name: "authorized", code: darwinStatusAuthorized, want: PermissionAuthorized},
		{name: "denied", code: darwinStatusDenied, want: PermissionDenied},
		{name: "not-determined", code: darwinStatusNotDetermined, want: PermissionNotDetermined},
		{name: "unknown", code: darwinStatusUnknown, want: PermissionUnknown},
		{name: "out-of-range code is unknown, not denied", code: 99, want: PermissionUnknown},
		{name: "negative code is unknown, not denied", code: -1, want: PermissionUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := darwinAuthorizationStatusToPermission(tt.code); got != tt.want {
				t.Errorf("darwinAuthorizationStatusToPermission(%d) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestPermissionStatusValues(t *testing.T) {
	// The frontend switches on these exact strings; a typo here
	// silently breaks the Settings row.
	want := map[PermissionStatus]string{
		PermissionAuthorized:    "authorized",
		PermissionDenied:        "denied",
		PermissionNotDetermined: "not-determined",
		PermissionUnknown:       "unknown",
	}
	for status, str := range want {
		if string(status) != str {
			t.Errorf("PermissionStatus %q, want %q", string(status), str)
		}
	}
}
