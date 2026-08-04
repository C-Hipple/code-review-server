package pluginkit

import "testing"

func TestParseCallType(t *testing.T) {
	tests := []struct {
		in   string
		want CallType
	}{
		{"automatic", CallTypeAutomatic},
		{"explicit", CallTypeExplicit},
		{"rerun", CallTypeRerun},
		{"", CallTypeAutomatic},
		{"something-new", CallTypeAutomatic},
	}
	for _, tt := range tests {
		if got := ParseCallType(tt.in); got != tt.want {
			t.Errorf("ParseCallType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCallTypeRequested(t *testing.T) {
	if CallTypeAutomatic.Requested() {
		t.Error("automatic runs should not report as requested")
	}
	if !CallTypeExplicit.Requested() {
		t.Error("explicit runs should report as requested")
	}
	if !CallTypeRerun.Requested() {
		t.Error("reruns should report as requested")
	}
}
