//go:build darwin

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	launchDaemonDir    = "/Library/LaunchDaemons"
	launchdLabelPrefix = "com.postalsys."
)

// launchdLabel maps a service name to its launchd label. A name that already
// looks like a reverse-DNS label is used verbatim.
func launchdLabel(serviceName string) string {
	if strings.Contains(serviceName, ".") {
		return serviceName
	}
	return launchdLabelPrefix + serviceName
}

// plistPath returns the LaunchDaemon plist path for a service name.
func plistPath(serviceName string) string {
	return filepath.Join(launchDaemonDir, launchdLabel(serviceName)+".plist")
}

// isRootImpl checks if running as root on macOS.
func isRootImpl() bool {
	return os.Getuid() == 0
}

// installImpl installs the service on macOS using launchd.
func installImpl(cfg ServiceConfig, execPath string) error {
	label := launchdLabel(cfg.Name)
	path := plistPath(cfg.Name)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("service %s is already installed at %s", cfg.Name, path)
	}

	plist := generateLaunchdPlist(cfg, execPath, label)
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		return fmt.Errorf("failed to write launchd plist: %w", err)
	}
	// LaunchDaemons must be owned root:wheel (gid 0 on macOS).
	if err := os.Chown(path, 0, 0); err != nil {
		os.Remove(path)
		return fmt.Errorf("failed to set plist ownership: %w", err)
	}

	fmt.Printf("Created launchd daemon: %s\n", path)

	if output, err := runCommand("launchctl", "bootstrap", "system", path); err != nil {
		os.Remove(path)
		return fmt.Errorf("failed to bootstrap service: %s: %w", strings.TrimSpace(output), err)
	}
	// Best-effort: clear any leftover disabled flag so it starts on boot.
	runCommand("launchctl", "enable", "system/"+label)

	fmt.Printf("Bootstrapped service: %s\n", label)
	fmt.Println("\nService management commands:")
	fmt.Printf("  sudo launchctl print system/%s    # Check status\n", label)
	fmt.Printf("  sudo launchctl kickstart -k system/%s   # Restart\n", label)
	fmt.Printf("  sudo launchctl bootout system/%s  # Stop and unload\n", label)

	return nil
}

// uninstallImpl removes the launchd service on macOS.
func uninstallImpl(serviceName string) error {
	label := launchdLabel(serviceName)
	path := plistPath(serviceName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("service %s is not installed", serviceName)
	}

	// Stop and unload (ignore errors: it may not be loaded).
	if output, err := runCommand("launchctl", "bootout", "system/"+label); err != nil {
		if !strings.Contains(output, "No such process") && !strings.Contains(output, "Could not find") {
			fmt.Printf("Note: could not bootout service: %s\n", strings.TrimSpace(output))
		}
	} else {
		fmt.Printf("Booted out service: %s\n", label)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to remove launchd plist: %w", err)
	}
	fmt.Printf("Removed launchd daemon: %s\n", path)

	return nil
}

// statusImpl returns a coarse status string on macOS.
func statusImpl(serviceName string) (string, error) {
	info, err := statusDetailedImpl(serviceName)
	if err != nil {
		return "", err
	}
	if !info.Installed {
		return "not installed", nil
	}
	if info.Active {
		return "running", nil
	}
	if info.SubState != "" {
		return info.SubState, nil
	}
	return "not running", nil
}

// statusDetailedImpl populates StatusInfo from `launchctl print`. The daemon is
// "Installed" when its plist is present, "Enabled"/bootstrapped when launchctl
// can print it, and "Active" when its run state is "running".
func statusDetailedImpl(serviceName string) (StatusInfo, error) {
	out := StatusInfo{Installed: isInstalledImpl(serviceName)}

	label := launchdLabel(serviceName)
	printed, err := runCommand("launchctl", "print", "system/"+label)
	if err != nil {
		// Not bootstrapped (e.g. plist on disk but not loaded).
		return out, nil
	}

	out.Enabled = true
	state := launchdState(printed)
	out.SubState = state
	out.ActiveState = state
	out.Active = state == "running"
	return out, nil
}

// launchdState extracts the "state = <value>" field from `launchctl print`.
func launchdState(printed string) string {
	for _, line := range strings.Split(printed, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "state = "); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// isInstalledImpl checks if the service plist is present on macOS.
func isInstalledImpl(serviceName string) bool {
	_, err := os.Stat(plistPath(serviceName))
	return err == nil
}

// generateLaunchdPlist builds the LaunchDaemon plist for Mutiauk. The
// daemon runs in the foreground (the same `daemon start` invocation systemd
// uses with Type=simple) and launchd keeps it alive across non-clean exits.
func generateLaunchdPlist(cfg ServiceConfig, execPath, label string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
		<string>start</string>
		<string>-c</string>
		<string>%s</string>
	</array>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>/var/log/%s.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/%s.err.log</string>
</dict>
</plist>
`, label, execPath, cfg.ConfigPath, cfg.WorkingDir, cfg.Name, cfg.Name)
}
