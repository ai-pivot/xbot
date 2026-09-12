package main

// feishu_bind.go — one-click Feishu agent-app registration / rebinding.
//
//	xbot-cli feishu-bind                          # create a NEW Feishu agent app
//	xbot-cli feishu-bind --app-id cli_xxx         # bind/upgrade an EXISTING app
//
// Uses the Lark SDK device-authorization flow (registration.RegisterApp,
// RFC 8628): the user opens the printed link (or scans it as a QR code) and
// confirms; the app then carries the full agent preset — permissions, event
// subscriptions and the card callback — and the credentials are printed and
// (unless --no-save) written into ~/.xbot/config.json under channels.feishu.
//
// The link expires after ~10 minutes and can be used by exactly one person.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"xbot/config"
	log "xbot/logger"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

// feishuAgentTenantScopes is the permission set xbot's agent needs on top of
// the platform default template. cardkit:card:* powers the CardKit streaming
// progress card; the im:*/docs:* scopes power the bot itself.
var feishuAgentTenantScopes = []string{
	"im:message:send_as_bot",
	"im:message:readonly",
	"im:message:update",
	"im:message:send_multi_users",
	"im:message:send_sys_msg",
	"im:message.p2p_msg:readonly",
	"im:message.group_at_msg:readonly",
	"im:message.group_at_msg.include_bot:readonly",
	"im:message.pins:read",
	"im:message.pins:write_only",
	"im:message.reactions:read",
	"im:message.reactions:write_only",
	"im:chat:read",
	"im:chat:create",
	"im:chat:update",
	"im:chat.members:bot_access",
	"im:resource",
	"cardkit:card:read",
	"cardkit:card:write",
	"contact:contact.base:readonly",
	"application:application:self_manage",
	"application:bot.basic_info:read",
	"application:bot.menu:write",
	"application:app_slash_command:read",
	"application:app_slash_command:write",
	"drive:drive.metadata:readonly",
	"docs:document.comment:create",
	"docs:document.comment:read",
	"docs:document.comment:write_only",
	"docx:document:readonly",
	"docx:document:write_only",
	"docx:document.block:convert",
	"wiki:node:read",
}

// feishuAgentTenantEvents is the event subscription set the bot listens on.
var feishuAgentTenantEvents = []string{
	"im.message.receive_v1",
	"im.message.reaction.created_v1",
	"im.message.reaction.deleted_v1",
	"im.chat.member.bot.added_v1",
	"im.chat.member.bot.deleted_v1",
}

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

	if target != "" {
		fmt.Printf("Binding the agent capabilities onto the EXISTING app %s\n", target)
	} else {
		fmt.Println("Creating a NEW Feishu agent app")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := registration.RegisterApp(ctx, &registration.Options{
		Source:     "xbot",
		AppID:      target,
		CreateOnly: *createOnly,
		Addons: &registration.AppAddons{
			Scopes: registration.AppAddonsScopes{
				Tenant: feishuAgentTenantScopes,
				User:   []string{"offline_access"},
			},
			Events: registration.AppAddonsEvents{
				Items: registration.AppAddonsEventItems{Tenant: feishuAgentTenantEvents},
			},
			Callbacks: registration.AppAddonsCallbacks{Items: []string{"card.action.trigger"}},
		},
		OnQRCode: func(info *registration.QRCodeInfo) {
			// The whole flow hinges on the user opening this link, so it goes
			// to stdout unadorned (scripts/tests read the URL line directly).
			fmt.Printf("URL: %s\n", info.URL)
			fmt.Printf("Expires in %d seconds — open it (or scan it as a QR code) and confirm.\n", info.ExpireIn)
		},
		OnStatusChange: func(info *registration.StatusChangeInfo) {
			if info.Interval > 0 {
				log.Debugf("feishu-bind: %s (next poll in %ds)", info.Status, info.Interval)
			}
		},
	})
	if err != nil {
		var regErr *registration.RegisterAppError
		if errors.As(err, &regErr) {
			return fmt.Errorf("feishu app registration failed: code=%s description=%s", regErr.Code, regErr.Description)
		}
		var denied *registration.AccessDeniedError
		if errors.As(err, &denied) {
			return fmt.Errorf("feishu app registration denied by the user: %w", err)
		}
		var expired *registration.ExpiredError
		if errors.As(err, &expired) {
			return fmt.Errorf("feishu app registration link expired before confirmation: %w", err)
		}
		return fmt.Errorf("feishu app registration failed: %w", err)
	}

	fmt.Printf("App ID:     %s\n", result.ClientID)
	fmt.Printf("App Secret: %s\n", result.ClientSecret)

	if *noSave || cfg == nil {
		return nil
	}
	if result.ClientID != "" {
		cfg.Feishu.AppID = result.ClientID
	}
	if result.ClientSecret != "" {
		cfg.Feishu.AppSecret = result.ClientSecret
	}
	cfg.Feishu.Enabled = true
	if err := config.SaveToFile(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config %s: %w", cfgPath, err)
	}
	fmt.Printf("Saved to %s (channels.feishu.app_id / app_secret, enabled=true).\n", cfgPath)
	fmt.Println("Restart the server for the new credentials to take effect.")
	return nil
}
