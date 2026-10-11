// Package wizard provides an interactive setup wizard for Mutiauk.
package wizard

import (
	"fmt"
	"runtime"

	"github.com/Muti-Metroo/Mutiauk/internal/service"
	"github.com/Muti-Metroo/Mutiauk/internal/wizard/prompt"
)

// ServiceInstaller handles the interactive service installation flow.
type ServiceInstaller struct {
	configPath string
}

// NewServiceInstaller creates a new service installer for the given config path.
func NewServiceInstaller(configPath string) *ServiceInstaller {
	return &ServiceInstaller{configPath: configPath}
}

// Run executes the interactive service installation flow.
// Returns nil on success or if the user declines installation.
func (s *ServiceInstaller) Run() error {
	prompt.PrintHeader("Service Installation", "Install Mutiauk as a system service.")

	if !s.supported() {
		prompt.PrintInfo(fmt.Sprintf("Service installation is not available on %s.", runtime.GOOS))
		return nil
	}

	if !s.isRoot() {
		s.printNonRootInstructions()
		return nil
	}

	return s.promptAndInstall()
}

// supported reports whether the current OS has a service backend
// (systemd on Linux, launchd on macOS, the SCM on Windows).
func (s *ServiceInstaller) supported() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		return true
	default:
		return false
	}
}

func (s *ServiceInstaller) isRoot() bool {
	return service.IsRoot()
}

func (s *ServiceInstaller) printNonRootInstructions() {
	if runtime.GOOS == "windows" {
		prompt.PrintWarning("Not running as Administrator. Service installation requires an elevated prompt.")
	} else {
		prompt.PrintWarning("Not running as root. Service installation requires root privileges.")
	}
	fmt.Println()
	s.printManualInstallCommand()
}

func (s *ServiceInstaller) printManualInstallCommand() {
	fmt.Println("To install as a service later, run:")
	fmt.Printf("  %s\n", s.installCommand())
}

// installCommand returns the OS-appropriate `service install` invocation
// (sudo on Unix; an elevated prompt is assumed on Windows).
func (s *ServiceInstaller) installCommand() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("mutiauk service install -c %s   (from an elevated prompt)", s.configPath)
	}
	return fmt.Sprintf("sudo mutiauk service install -c %s", s.configPath)
}

func (s *ServiceInstaller) promptAndInstall() error {
	install, err := prompt.Confirm("Install Mutiauk as a system service?", true)
	if err != nil {
		return err
	}

	if !install {
		fmt.Println()
		s.printManualInstallCommand()
		return nil
	}

	if service.IsInstalled("mutiauk") {
		prompt.PrintWarning("Service is already installed. Uninstall first to reinstall.")
		return nil
	}

	return s.install()
}

func (s *ServiceInstaller) install() error {
	fmt.Println()
	prompt.PrintInfo("Installing system service...")

	cfg := service.DefaultConfig(s.configPath)
	if err := service.Install(cfg); err != nil {
		prompt.PrintError(fmt.Sprintf("Failed to install service: %v", err))
		fmt.Println()
		fmt.Println("You can try installing manually:")
		fmt.Printf("  %s\n", s.installCommand())
		return nil // Don't fail the wizard for service install failure
	}

	prompt.PrintSuccess("Service installed and started!")
	s.printServiceManagementCommands()

	return nil
}

func (s *ServiceInstaller) printServiceManagementCommands() {
	fmt.Println()
	fmt.Println("Service management commands:")
	switch runtime.GOOS {
	case "windows":
		fmt.Println("  sc query mutiauk     # Check status")
		fmt.Println("  sc stop mutiauk      # Stop service")
		fmt.Println("  sc start mutiauk     # Start service")
	case "darwin":
		fmt.Println("  sudo launchctl print system/com.postalsys.mutiauk   # Check status")
		fmt.Println("  sudo launchctl kickstart -k system/com.postalsys.mutiauk  # Restart")
		fmt.Println("  sudo launchctl bootout system/com.postalsys.mutiauk # Stop")
	default:
		fmt.Println("  sudo systemctl status mutiauk    # Check status")
		fmt.Println("  sudo systemctl stop mutiauk      # Stop service")
		fmt.Println("  sudo systemctl restart mutiauk   # Restart service")
		fmt.Println("  sudo journalctl -u mutiauk -f    # View logs")
	}
}
