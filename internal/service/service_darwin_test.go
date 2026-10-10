//go:build darwin

package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLaunchdLabel(t *testing.T) {
	if got := launchdLabel("mutiauk"); got != "com.postalsys.mutiauk" {
		t.Errorf("launchdLabel(mutiauk) = %q", got)
	}
	if got := launchdLabel("com.example.svc"); got != "com.example.svc" {
		t.Errorf("launchdLabel of reverse-DNS name should pass through, got %q", got)
	}
}

func TestPlistPath(t *testing.T) {
	want := "/Library/LaunchDaemons/com.postalsys.mutiauk.plist"
	if got := plistPath("mutiauk"); got != want {
		t.Errorf("plistPath = %q, want %q", got, want)
	}
}

func TestGenerateLaunchdPlist(t *testing.T) {
	cfg := ServiceConfig{
		Name:        "mutiauk",
		ConfigPath:  "/etc/mutiauk/config.yaml",
		WorkingDir:  "/etc/mutiauk",
		Description: "Mutiauk",
	}
	plist := generateLaunchdPlist(cfg, "/usr/local/bin/mutiauk", "com.postalsys.mutiauk")

	for _, want := range []string{
		"<key>Label</key>",
		"<string>com.postalsys.mutiauk</string>",
		"<string>/usr/local/bin/mutiauk</string>",
		"<string>/etc/mutiauk/config.yaml</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q\n%s", want, plist)
		}
	}
}

func TestLaunchdState(t *testing.T) {
	printed := `system/com.postalsys.mutiauk = {
	active count = 1
	state = running
	program = /usr/local/bin/mutiauk
}`
	if got := launchdState(printed); got != "running" {
		t.Errorf("launchdState = %q, want running", got)
	}
	if got := launchdState("no state here"); got != "" {
		t.Errorf("launchdState with no state = %q, want empty", got)
	}
}

func TestStatusDetailed_NotInstalled(t *testing.T) {
	info, err := statusDetailedImpl("definitely-not-a-real-service-xyz")
	if err != nil {
		t.Fatalf("statusDetailedImpl: %v", err)
	}
	if info.Installed || info.Active || info.Enabled {
		t.Errorf("expected all-false for missing service, got %+v", info)
	}
}

// TestLaunchdLifecycle bootstraps a throwaway LaunchDaemon (running /bin/sleep)
// and verifies the status-read path against real launchd. It needs root and is
// skipped otherwise. The label is unique so it never collides with a real
// mutiauk install.
func TestLaunchdLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to bootstrap a LaunchDaemon")
	}

	const name = "mutiauk-lifecycle-test"
	label := launchdLabel(name)
	path := plistPath(name)

	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + label + `</string>
	<key>ProgramArguments</key>
	<array><string>/bin/sleep</string><string>3600</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	defer func() {
		runCommand("launchctl", "bootout", "system/"+label)
		os.Remove(path)
	}()

	if !IsInstalled(name) {
		t.Fatal("IsInstalled = false after writing plist")
	}
	if out, err := runCommand("launchctl", "bootstrap", "system", path); err != nil {
		t.Fatalf("bootstrap: %v (%s)", err, out)
	}

	// launchd briefly reports "xpcproxy" while exec'ing the target; poll until
	// the run state settles.
	var info StatusInfo
	for i := 0; i < 30; i++ {
		var err error
		if info, err = StatusDetailed(name); err != nil {
			t.Fatalf("StatusDetailed: %v", err)
		}
		if info.Active {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !info.Installed {
		t.Error("Installed = false")
	}
	if !info.Enabled {
		t.Error("Enabled = false (service should be bootstrapped)")
	}
	if !info.Active {
		t.Errorf("Active = false after polling; sub_state=%q (sleep should be running)", info.SubState)
	}
}
