//go:build windows

package uninstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func platformServiceLabel() string {
	return "Remove startup shortcut / scheduled task / nssm service"
}

func stopService() error {
	// Try to stop the process
	cmd := exec.Command("taskkill", "/F", "/IM", "xbot-cli.exe")
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// Also try nssm service if present
	home, _ := os.UserHomeDir()
	nssmPath := filepath.Join(home, ".local", "bin", "nssm.exe")
	if _, err := os.Stat(nssmPath); err == nil {
		_ = exec.Command(nssmPath, "stop", "xbot-server").Run()
	}
	// Try scheduled task
	_ = exec.Command("schtasks", "/End", "/TN", "xbot-server").Run()

	fmt.Println("  Attempted to stop xbot-cli processes.")
	return nil
}

func removeService() error {
	home, _ := os.UserHomeDir()
	xbotHome := filepath.Join(home, ".xbot")

	// 1. Remove Startup folder shortcut
	startupFolder := os.Getenv("APPDATA")
	if startupFolder != "" {
		shortcut := filepath.Join(startupFolder, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "xbot-server.lnk")
		if _, err := os.Stat(shortcut); err == nil {
			if err := os.Remove(shortcut); err != nil {
				fmt.Printf("  Warning: failed to remove startup shortcut: %v\n", err)
			} else {
				fmt.Println("  Removed startup shortcut.")
			}
		}
	}

	// 2. Remove scheduled task (if exists)
	cmd := exec.Command("schtasks", "/Query", "/TN", "xbot-server")
	if cmd.Run() == nil {
		_ = exec.Command("schtasks", "/Delete", "/TN", "xbot-server", "/F").Run()
		fmt.Println("  Removed scheduled task.")
	}

	// 3. Remove nssm service (if registered)
	nssmPath := filepath.Join(home, ".local", "bin", "nssm.exe")
	if _, err := os.Stat(nssmPath); err == nil {
		_ = exec.Command(nssmPath, "remove", "xbot-server", "confirm").Run()
		fmt.Println("  Removed nssm service (if registered).")
	}

	// 4. Remove helper scripts
	scriptsDir := filepath.Join(xbotHome, "scripts")
	if _, err := os.Stat(scriptsDir); err == nil {
		_ = os.RemoveAll(scriptsDir)
		fmt.Println("  Removed helper scripts.")
	}

	return nil
}

func cleanPATH() error {
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")

	// Read current user PATH
	currentPath := os.Getenv("PATH")
	// Also check the persistent user env var
	cmd := exec.Command("powershell", "-Command",
		fmt.Sprintf("[Environment]::GetEnvironmentVariable('Path', 'User')"))
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("  Could not read user PATH, skipping.")
		return nil
	}
	userPathStr := strings.TrimSpace(string(output))
	entries := strings.Split(userPathStr, ";")

	var newEntries []string
	for _, entry := range entries {
		cleaned := strings.TrimSpace(entry)
		if cleaned == "" {
			continue
		}
		// Compare case-insensitively on Windows
		if !strings.EqualFold(cleaned, localBin) {
			newEntries = append(newEntries, cleaned)
		}
	}

	newPath := strings.Join(newEntries, ";")
	if newPath == userPathStr {
		fmt.Println("  No PATH entries to clean.")
		return nil
	}

	// Set the new user PATH
	setCmd := exec.Command("powershell", "-Command",
		fmt.Sprintf("[Environment]::SetEnvironmentVariable('Path', '%s', 'User')", newPath))
	if err := setCmd.Run(); err != nil {
		return fmt.Errorf("failed to update user PATH: %w", err)
	}
	fmt.Println("  Cleaned .local\\bin from user PATH.")
	_ = currentPath // suppress unused warning
	return nil
}
