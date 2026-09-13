package main

// feishu_bind.go — one-click Feishu agent-app registration / rebinding.
//
//	xbot-cli feishu-bind                          # create a NEW Feishu agent app
//	xbot-cli feishu-bind --app-id cli_xxx         # bind/upgrade an EXISTING app
//
// Prints the authorization link from Feishu's device-authorization flow
// (RFC 8628): the user opens it (or scans it as a QR code) and confirms, and the
// app then carries the full agent preset — permissions (incl.
// cardkit:card:write), event subscriptions and the card callback. The
// credentials are printed and (unless --no-save) written into config.json.
//
// The link expires after ~10 minutes and can be used by exactly one person.
// The same flow backs the Web channels panel (serverapp.feishu_binder).

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"xbot/config"
	"xbot/internal/feishuapp"
	log "xbot/logger"
)

func runFeishuBind(args []string) error {
	fs := flag.NewFlagSet("feishu-bind", flag.ContinueOnError)
	appID := fs.String("app-id", "", "existing app id to bind/upgrade (default: channels.feishu.app_id from config)")
	createOnly := fs.Bool("create-only", false, "only allow creating a new app (never update an existing one)")
	noSave := fs.Bool("no-save", false, "print the credentials without writing config.json")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for the user to confirm")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgPath := config.ConfigFilePath()
	cfg := config.LoadFromFile(cfgPath)

	target := strings.TrimSpace(*appID)
	if target == "" && cfg != nil {
		target = strings.TrimSpace(cfg.Feishu.AppID)
	}
	if *createOnly {
		target = ""
	}

	if target != "" {
		fmt.Printf("Binding the agent capabilities onto the EXISTING app %s\n", target)
	} else {
		fmt.Println("Creating a NEW Feishu agent app")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := feishuapp.Register(ctx, target, func(url string, expireIn int) {
		// The whole flow hinges on the user opening this link, so it goes to
		// stdout unadorned (scripts/tests read the URL line directly).
		fmt.Printf("URL: %s\n", url)
		fmt.Printf("Expires in %d seconds — open it (or scan it as a QR code) and confirm.\n", expireIn)
	})
	if err != nil {
		return err
	}

	fmt.Printf("App ID:     %s\n", result.ClientID)
	fmt.Printf("App Secret: %s\n", result.ClientSecret)

	if *noSave {
		return nil
	}
	if err := feishuapp.SaveCredentials(cfgPath, result.ClientID, result.ClientSecret); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	log.Debugf("feishu-bind: saved credentials for %s", result.ClientID)
	fmt.Printf("Saved to %s (channels.feishu.app_id / app_secret, enabled=true).\n", cfgPath)
	fmt.Println("Restart the server for the new credentials to take effect.")
	return nil
}
