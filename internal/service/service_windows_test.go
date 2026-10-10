//go:build windows

package service

import (
	"testing"

	"golang.org/x/sys/windows/svc"
)

func TestStateString(t *testing.T) {
	tests := []struct {
		state svc.State
		want  string
	}{
		{svc.Stopped, "stopped"},
		{svc.Running, "running"},
		{svc.StartPending, "start-pending"},
		{svc.StopPending, "stop-pending"},
		{svc.Paused, "paused"},
	}
	for _, tt := range tests {
		if got := stateString(tt.state); got != tt.want {
			t.Errorf("stateString(%d) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestSupportedIncludesWindows(t *testing.T) {
	if !Supported() {
		t.Error("Supported() = false on windows, want true")
	}
}
