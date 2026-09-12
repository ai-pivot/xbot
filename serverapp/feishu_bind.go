package serverapp

// feishu_bind.go — server-side driver for the Web channels panel's
// "one-click Feishu" action.
//
// The Web panel cannot run the Lark device-authorization flow itself, so the
// server owns ONE bind attempt at a time: feishu_bind_start issues the link
// (returned synchronously) and keeps polling in the background; the panel polls
// feishu_bind_status until it reports done/error. On success the credentials are
// written to config.json (channels.feishu) exactly like the CLI subcommand
// `xbot-cli feishu-bind` does — both go through internal/feishuapp.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/config"
	"xbot/internal/feishuapp"
	log "xbot/logger"
)

const (
	// feishuBindLinkTimeout bounds how long feishu_bind_start waits for the
	// link to be issued (the device-authorization request is quick; a slow
	// network must not hang the HTTP handler forever).
	feishuBindLinkTimeout = 20 * time.Second

	// feishuBindSessionTimeout bounds the whole attempt — the user has
	// feishuapp.LinkLifetimeSeconds (~10 min) to confirm.
	feishuBindSessionTimeout = 11 * time.Minute
)

// feishuBindState is the lifecycle of one bind attempt.
type feishuBindState string

const (
	feishuBindIdle    feishuBindState = "idle"
	feishuBindWaiting feishuBindState = "waiting" // link issued, awaiting confirmation
	feishuBindDone    feishuBindState = "done"
	feishuBindError   feishuBindState = "error"
)

// feishuBinder owns the single in-flight bind attempt.
type feishuBinder struct {
	mu     sync.Mutex
	state  feishuBindState
	url    string
	appID  string
	errMsg string
	cancel context.CancelFunc
}

// globalFeishuBinder is the process-wide binder (one attempt at a time:
// the authorization link is single-use, so concurrent attempts would fight).
var globalFeishuBinder = &feishuBinder{state: feishuBindIdle}

// Start supersedes any previous attempt and returns the authorization link.
//
// appID empty → the panel asked to create a new app; appID set (or picked up
// from config) → bind/upgrade that existing app.
func (b *feishuBinder) Start(appID string) (string, error) {
	if strings.TrimSpace(appID) == "" {
		if cfg := config.LoadFromFile(config.ConfigFilePath()); cfg != nil {
			appID = strings.TrimSpace(cfg.Feishu.AppID)
		}
	}
	appID = strings.TrimSpace(appID)

	ctx, cancel := context.WithTimeout(context.Background(), feishuBindSessionTimeout)
	linkCh := make(chan string, 1)

	b.mu.Lock()
	if b.cancel != nil {
		b.cancel() // drop the previous attempt
	}
	b.cancel = cancel
	b.state = feishuBindWaiting
	b.url = ""
	b.appID = appID
	b.errMsg = ""
	b.mu.Unlock()

	go func() {
		defer cancel()
		result, err := feishuapp.Register(ctx, appID, func(url string, _ int) {
			select {
			case linkCh <- url:
			default: // already recorded
			}
		})
		if err != nil {
			b.finish(feishuBindError, "", err.Error())
			return
		}
		if err := feishuapp.SaveCredentials(config.ConfigFilePath(), result.ClientID, result.ClientSecret); err != nil {
			b.finish(feishuBindError, result.ClientID, fmt.Sprintf("save credentials: %v", err))
			return
		}
		log.WithField("app_id", result.ClientID).Info("Feishu app bound via web panel")
		b.finish(feishuBindDone, result.ClientID, "")
	}()

	select {
	case url := <-linkCh:
		b.mu.Lock()
		b.url = url
		expiresIn := feishuapp.LinkLifetimeSeconds
		b.mu.Unlock()
		_ = expiresIn
		return url, nil
	case <-time.After(feishuBindLinkTimeout):
		return "", fmt.Errorf("timed out waiting for the Feishu authorization link")
	case <-ctx.Done():
		return "", fmt.Errorf("feishu bind cancelled")
	}
}

// finish records the terminal state of an attempt, unless it was superseded.
func (b *feishuBinder) finish(state feishuBindState, appID, errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel == nil {
		return // superseded by a newer attempt
	}
	b.cancel = nil
	b.state = state
	if appID != "" {
		b.appID = appID
	}
	b.errMsg = errMsg
}

// Status returns a snapshot for the panel's poll.
func (b *feishuBinder) Status() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]any{
		"state":  string(b.state),
		"app_id": b.appID,
		"url":    b.url,
	}
	if b.errMsg != "" {
		out["error"] = b.errMsg
	}
	if b.state == feishuBindWaiting {
		out["expires_in"] = feishuapp.LinkLifetimeSeconds
	}
	return out
}

// registerChannelOpsHandlers wires the channel-management RPCs the Web channels
// panel needs on top of the existing get_channel_config / set_channel_config.
func registerChannelOpsHandlers(t RPCTable, h *RPCContext) {
	t["feishu_bind_start"] = h.requireAdmin(rpc1(func(ctx context.Context, p struct {
		AppID string `json:"app_id"`
	}) (any, error) {
		url, err := globalFeishuBinder.Start(p.AppID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"url":        url,
			"expires_in": feishuapp.LinkLifetimeSeconds,
			"app_id":     globalFeishuBinder.Status()["app_id"],
		}, nil
	}))

	t["feishu_bind_status"] = h.requireAdmin(rpc0(func(ctx context.Context) any {
		return globalFeishuBinder.Status()
	}))
}
