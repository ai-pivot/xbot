package uninstall

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Run executes the uninstall subcommand.
func Run(args []string) error {
	purge := false
	skipConfirm := false

	for _, arg := range args {
		switch arg {
		case "--purge":
			purge = true
		case "-y", "--yes":
			skipConfirm = true
		case "--help", "-h":
			printUsage()
			return nil
		}
	}

	fmt.Println("xbot uninstaller")
	fmt.Println("================")
	fmt.Println()

	// Show what will be removed
	steps := []string{"Stop xbot-server service (if running)"}
	steps = append(steps, platformServiceLabel())
	steps = append(steps, "Remove xbot-cli binary")
	steps = append(steps, "Clean PATH entries")
	if purge {
		steps = append(steps, "Remove ~/.xbot/ directory (config, data, logs)")
	} else {
		steps = append(steps, "Keep ~/.xbot/ directory (use --purge to remove)")
	}

	fmt.Println("The following steps will be performed:")
	for i, s := range steps {
		fmt.Printf("  %d. %s\n", i+1, s)
	}
	fmt.Println()

	if !skipConfirm {
		fmt.Print("Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Step 1: Stop the running service/process
	fmt.Println("\n[1] Stopping xbot-server...")
	if err := stopService(); err != nil {
		fmt.Printf("  Warning: %v\n", err)
	}

	// Step 2: Remove service registration
	fmt.Println("[2] Removing service registration...")
	if err := removeService(); err != nil {
		fmt.Printf("  Warning: %v\n", err)
	}

	// Step 3: Remove binary
	fmt.Println("[3] Removing binary...")
	if err := removeBinary(); err != nil {
		fmt.Printf("  Warning: %v\n", err)
	}

	// Step 4: Clean PATH
	fmt.Println("[4] Cleaning PATH...")
	if err := cleanPATH(); err != nil {
		fmt.Printf("  Warning: %v\n", err)
	}

	// Step 5 (optional): Remove ~/.xbot/
	if purge {
		fmt.Println("[5] Removing ~/.xbot/...")
		if err := removeDataDir(); err != nil {
			fmt.Printf("  Warning: %v\n", err)
		}
	}

	fmt.Println()
	fmt.Println("✓ Uninstall complete.")
	if !purge {
		home, _ := os.UserHomeDir()
		fmt.Printf("  ~/.xbot/ directory preserved. Remove manually if not needed:\n")
		fmt.Printf("    rm -rf %s\n", filepath.Join(home, ".xbot"))
	}
	return nil
}

func printUsage() {
	fmt.Println("Usage: xbot-cli uninstall [options]")
	fmt.Println()
	fmt.Println("Uninstall xbot from this system.")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --purge    Also remove ~/.xbot/ (config, database, logs)")
	fmt.Println("  -y, --yes  Skip confirmation prompt")
	fmt.Println("  --help     Show this help")
}

// removeDataDir removes the ~/.xbot/ directory.
func removeDataDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	xbotDir := filepath.Join(home, ".xbot")
	if _, err := os.Stat(xbotDir); os.IsNotExist(err) {
		fmt.Println("  ~/.xbot/ does not exist, skipping.")
		return nil
	}
	if err := os.RemoveAll(xbotDir); err != nil {
		return fmt.Errorf("failed to remove %s: %w", xbotDir, err)
	}
	fmt.Printf("  Removed %s\n", xbotDir)
	return nil
}

// getBinaryPath returns the expected binary path.
func getBinaryPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		return filepath.Join(home, ".local", "bin", "xbot-cli.exe")
	}
	return filepath.Join(home, ".local", "bin", "xbot-cli")
}

// removeBinary removes the xbot-cli binary.
func removeBinary() error {
	binPath := getBinaryPath()
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		fmt.Println("  Binary not found, skipping.")
		return nil
	}
	if err := os.Remove(binPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", binPath, err)
	}
	fmt.Printf("  Removed %s\n", binPath)

	// Try to remove .local/bin if empty
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	if entries, err := os.ReadDir(localBin); err == nil && len(entries) == 0 {
		os.Remove(localBin)
		fmt.Printf("  Removed empty directory %s\n", localBin)
		// Try .local if also empty
		localDir := filepath.Join(home, ".local")
		if entries, err := os.ReadDir(localDir); err == nil && len(entries) == 0 {
			os.Remove(localDir)
			fmt.Printf("  Removed empty directory %s\n", localDir)
		}
	}
	return nil
}
