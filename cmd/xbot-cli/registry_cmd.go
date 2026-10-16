package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/config"
	"xbot/registry"
	"xbot/tools"
)

// runRegistryCmd handles `xbot-cli registry <subcommand> [args]`.
func runRegistryCmd(args []string) error {
	if len(args) == 0 {
		printRegistryHelp()
		return nil
	}

	xbotHome := config.XbotHome()

	switch args[0] {
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: xbot-cli registry add <name> <url> [branch]")
		}
		branch := ""
		if len(args) >= 4 {
			branch = args[3]
		}
		if err := registry.AddRegistry(xbotHome, args[1], args[2], branch); err != nil {
			return err
		}
		fmt.Printf("✅ Added registry %q (%s)\n", args[1], args[2])
		fmt.Println("Run 'xbot-cli search' to browse available items.")
		return nil

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: xbot-cli registry remove <name>")
		}
		if err := registry.RemoveRegistry(xbotHome, args[1]); err != nil {
			return err
		}
		fmt.Printf("✅ Removed registry %q\n", args[1])
		return nil

	case "list":
		cfg, err := registry.LoadConfig(xbotHome)
		if err != nil {
			return err
		}
		if len(cfg.Registries) == 0 {
			fmt.Println("No registries configured. Add one with: xbot-cli registry add <name> <url>")
			return nil
		}
		fmt.Println("Configured registries:")
		for _, r := range cfg.Registries {
			branch := r.Branch
			if branch == "" {
				branch = registry.DefaultBranch
			}
			fmt.Printf("  %s  %s (branch: %s)\n", r.Name, r.URL, branch)
		}
		return nil

	default:
		printRegistryHelp()
		return nil
	}
}

// runSearchCmd handles `xbot-cli search [query]`.
func runSearchCmd(args []string) error {
	xbotHome := config.XbotHome()

	cfg, err := registry.LoadConfig(xbotHome)
	if err != nil {
		return err
	}
	if len(cfg.Registries) == 0 {
		fmt.Println("No registries configured. Add one with: xbot-cli registry add <name> <url>")
		return nil
	}

	fmt.Println("Fetching registry index...")
	idx, err := registry.Fetch(xbotHome)
	if err != nil {
		return err
	}

	query := strings.TrimSpace(strings.Join(args, " "))
	results := registry.Search(idx, query)

	if len(results) == 0 {
		fmt.Printf("No items found for %q\n", query)
		return nil
	}

	fmt.Printf("Found %d item(s):\n\n", len(results))
	for _, e := range results {
		fmt.Printf("  %s\n", registry.FormatEntry(e))
	}
	fmt.Println("\nInstall with: xbot-cli install <name>")
	return nil
}

// runInstallFromRegistry handles `xbot-cli install <name> [--force]`.
func runInstallFromRegistry(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: xbot-cli install <name> [--force]")
	}

	name := args[0]
	force := false
	for _, a := range args[1:] {
		if a == "--force" || a == "-f" {
			force = true
		}
	}

	xbotHome := config.XbotHome()
	workDir := getWorkDir()

	// Fetch registry index
	idx, err := registry.Fetch(xbotHome)
	if err != nil {
		return err
	}

	entry := registry.FindEntry(idx, name)
	if entry == nil {
		fmt.Printf("Item %q not found in any registry.\n", name)
		fmt.Println("Search with: xbot-cli search <query>")
		return nil
	}

	fmt.Printf("Installing %s/%s from registry %q...\n", entry.Type, entry.Name, entry.Registry)

	if force {
		// Remove existing installation first
		removeExisting(entry, xbotHome, workDir, cliSenderID)
	}

	if err := registry.Install(xbotHome, workDir, cliSenderID, entry); err != nil {
		return err
	}

	fmt.Printf("✅ Installed %s %q (v%s)\n", entry.Type, entry.Name, entry.Version)
	fmt.Println("Use /plugin reload-all if you installed a plugin and need it active immediately.")
	return nil
}

// runListInstalled handles `xbot-cli list-installed`.
func runListInstalled(args []string) error {
	xbotHome := config.XbotHome()
	workDir := getWorkDir()

	// List skills
	skillsDir := tools.UserSkillsRoot(workDir, cliSenderID)
	skills := listDirNames(skillsDir)

	// List agents
	agentsDir := tools.UserAgentsRoot(workDir, cliSenderID)
	agents := listMDFiles(agentsDir)

	// List plugins
	pluginsDir := filepath.Join(xbotHome, "plugins")
	plugins := listDirNames(pluginsDir)

	if len(skills) == 0 && len(agents) == 0 && len(plugins) == 0 {
		fmt.Println("No items installed. Browse with: xbot-cli search")
		return nil
	}

	if len(skills) > 0 {
		fmt.Println("Skills:")
		for _, s := range skills {
			fmt.Printf("  %s\n", s)
		}
		fmt.Println()
	}
	if len(agents) > 0 {
		fmt.Println("Agents:")
		for _, a := range agents {
			fmt.Printf("  %s\n", a)
		}
		fmt.Println()
	}
	if len(plugins) > 0 {
		fmt.Println("Plugins:")
		for _, p := range plugins {
			fmt.Printf("  %s\n", p)
		}
	}
	return nil
}

// getWorkDir returns the configured work directory or the current directory.
func getWorkDir() string {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg != nil && cfg.Agent.WorkDir != "" {
		return cfg.Agent.WorkDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// removeExisting removes a previously installed item (for --force install).
func removeExisting(entry *registry.Entry, xbotHome, workDir, senderID string) {
	switch entry.Type {
	case "skill":
		dir := filepath.Join(tools.UserSkillsRoot(workDir, senderID), entry.Name)
		_ = os.RemoveAll(dir)
	case "agent":
		file := filepath.Join(tools.UserAgentsRoot(workDir, senderID), entry.Name+".md")
		_ = os.Remove(file)
	case "plugin":
		dir := filepath.Join(xbotHome, "plugins", entry.Name)
		_ = os.RemoveAll(dir)
	}
}

// listDirNames returns the names of subdirectories in dir.
func listDirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// listMDFiles returns file names (without .md) in dir.
func listMDFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	return names
}

func printRegistryHelp() {
	fmt.Println("Registry commands:")
	fmt.Println("  xbot-cli registry add <name> <url> [branch]  Add a registry")
	fmt.Println("  xbot-cli registry remove <name>            Remove a registry")
	fmt.Println("  xbot-cli registry list                      List configured registries")
	fmt.Println()
	fmt.Println("Distribution commands:")
	fmt.Println("  xbot-cli search [query]                     Search registries")
	fmt.Println("  xbot-cli install <name> [--force]          Install from registry")
	fmt.Println("  xbot-cli list-installed                     List installed items")
}
