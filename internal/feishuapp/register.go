// Package feishuapp provisions Feishu (Lark) apps for xbot through the official
// device-authorization flow (RFC 8628) exposed by the Lark SDK as
// registration.RegisterApp.
//
// Both entry points share this package so the agent permission/event/callback
// preset lives in exactly one place:
//
//   - the CLI subcommand `xbot-cli feishu-bind` (terminal link)
//   - the Web channels panel (`feishu_bind_start` RPC, in-server link + poll)
package feishuapp

import (
	"context"
	"errors"
	"fmt"

	"xbot/config"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

// LinkLifetimeSeconds is how long a freshly issued authorization link stays
// valid (Feishu's own limit for the launcher URL).
const LinkLifetimeSeconds = 600

// tenantScopes is the app-identity permission set xbot's agent needs on top of
// the platform default template. cardkit:card:* powers the CardKit streaming
// progress card; the im:*/docs:* scopes power the bot itself.
var tenantScopes = []string{
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

// tenantEvents is the event subscription set the bot listens on.
var tenantEvents = []string{
	"im.message.receive_v1",
	"im.message.reaction.created_v1",
	"im.message.reaction.deleted_v1",
	"im.chat.member.bot.added_v1",
	"im.chat.member.bot.deleted_v1",
}

// Addons returns the incremental agent preset applied on top of the platform
// default template.
func Addons() *registration.AppAddons {
	return &registration.AppAddons{
		Scopes: registration.AppAddonsScopes{
			Tenant: append([]string(nil), tenantScopes...),
			User:   []string{"offline_access"},
		},
		Events: registration.AppAddonsEvents{
			Items: registration.AppAddonsEventItems{
				Tenant: append([]string(nil), tenantEvents...),
			},
		},
		Callbacks: registration.AppAddonsCallbacks{Items: []string{"card.action.trigger"}},
	}
}

// Register runs one device-authorization registration.
//
// appID empty → create a NEW agent app. appID set → bind/upgrade that EXISTING
// app (the incremental confirmation flow).
//
// onLink is invoked once the authorization link is ready; it must return
// promptly (the SDK blocks on it for the first poll). A nil onLink is valid.
func Register(ctx context.Context, appID string, onLink func(url string, expireIn int)) (*registration.RegisterAppResult, error) {
	opts := &registration.Options{
		Source: "xbot",
		AppID:  appID,
		Addons: Addons(),
	}
	if onLink != nil {
		opts.OnQRCode = func(info *registration.QRCodeInfo) {
			if info != nil {
				onLink(info.URL, info.ExpireIn)
			}
		}
	}
	result, err := registration.RegisterApp(ctx, opts)
	if err != nil {
		return nil, DescribeError(err)
	}
	return result, nil
}

// DescribeError turns the SDK's typed registration errors into a readable
// message (the SDK error itself carries only a code + description).
func DescribeError(err error) error {
	var regErr *registration.RegisterAppError
	if errors.As(err, &regErr) {
		return fmt.Errorf("feishu app registration failed: code=%s description=%s", regErr.Code, regErr.Description)
	}
	return err
}

// SaveCredentials writes the returned credentials into the xbot config file
// (channels.feishu) and enables the channel.
func SaveCredentials(cfgPath, appID, appSecret string) error {
	cfg := config.LoadFromFile(cfgPath)
	if cfg == nil {
		return fmt.Errorf("config not found: %s", cfgPath)
	}
	if appID != "" {
		cfg.Feishu.AppID = appID
	}
	if appSecret != "" {
		cfg.Feishu.AppSecret = appSecret
	}
	cfg.Feishu.Enabled = true
	return config.SaveToFile(cfgPath, cfg)
}
