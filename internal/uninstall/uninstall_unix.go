//go:build !windows

package uninstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func platformServiceLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "Remove launchd LaunchAgent plist"
	case "linux":
		return "Remove systemd user unit"
	default:
		return "Remove service registration"
	}
}

func stopService() error {
	switch runtime.GOOS {
	case "linux":
		return stopSystemd()
	case "darwin":
		return stopLaunchd()
	default:
		return killProcess()
	}
}

func stopSystemd() error {
	cmd := exec.Command("systemctl", "--user", "stop", "xbot-server")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Service might not exist, try killing the process
		return killProcess()
	}
	fmt.Println("  Stopped systemd xbot-server service.")
	return nil
}

func stopLaunchd() error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.xbot.server.plist")
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return killProcess()
	}
	cmd := exec.Command("launchctl", "unload", "-w", plistPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl unload failed: %w", err)
	}
	fmt.Println("  Unloaded launchd service.")
	return nil
}

func killProcess() error {
	// Try pkill xbot-cli
	cmd := exec.Command("pkill", "-f", "xbot-cli")
	_ = cmd.Run() // Ignore error if no process found
	fmt.Println("  Attempted to stop xbot-cli processes.")
	return nil
}

func removeService() error {
	switch runtime.GOOS {
	case "linux":
		return removeSystemdUnit()
	case "darwin":
		return removeLaunchdPlist()
	default:
		return nil
	}
}

func removeSystemdUnit() error {
	home, _ := os.UserHomeDir()
	unitPath := filepath.Join(home, ".config", "systemd", "user", "xbot-server.service")
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		fmt.Println("  systemd unit not found, skipping.")
		return nil
	}

	// Disable first
	_ = exec.Command("systemctl", "--user", "disable", "xbot-server").Run()

	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", unitPath, err)
	}
	fmt.Printf("  Removed %s\n", unitPath)

	// Reload systemd
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("  Reloaded systemd daemon.")
	return nil
}

func removeLaunchdPlist() error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.xbot.server.plist")
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		fmt.Println("  launchd plist not found, skipping.")
		return nil
	}
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", plistPath, err)
	}
	fmt.Printf("  Removed %s\n", plistPath)
	return nil
}

func cleanPATH() error {
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")

	// Determine shell config file
	shell := os.Getenv("SHELL")
	var profiles []string
	if strings.Contains(shell, "zsh") {
		profiles = []string{filepath.Join(home, ".zshrc")}
	} else {
		profiles = []string{filepath.Join(home, ".bashrc"), filepath.Join(home, ".bash_profile")}
	}

	cleaned := false
	for _, profile := range profiles {
		if _, err := os.Stat(profile); os.IsNotExist(err) {
			continue
		}
		if err := removePathEntry(profile, localBin); err != nil {
			fmt.Printf("  Warning: failed to clean %s: %v\n", profile, err)
			continue
		}
		cleaned = true
	}
	if !cleaned {
		fmt.Println("  No PATH entries to clean.")
	}
	return nil
}

// removePathEntry removes lines containing the given path from a shell config file.
func removePathEntry(profilePath, targetPath string) error {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	var kept []string
	changed := false

	for _, line := range lines {
		if strings.Contains(line, targetPath) && (strings.Contains(line, "export") || strings.Contains(line, "PATH")) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}

	if !changed {
		return nil
	}

	// Write back
	result := strings.Join(kept, "\n")
	// Avoid trailing newlines growing
	result = strings.TrimRight(result, "\n") + "\n"
	if err := os.WriteFile(profilePath, []byte(result), 0o644); err != nil {
		return err
	}
	fmt.Printf("  Cleaned PATH entry from %s\n", filepath.Base(profilePath))
	return nil
}
