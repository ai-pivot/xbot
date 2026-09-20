package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestManifestPermissions guards the frontend capability declaration.
//
// 真实事故（2026-09-18 用户报告「genui 插件之前不是加了分享功能吗？我现在怎么看不到
// 分享按钮了」）：分享 UI（`ShareablePanel` 的分享按钮 + `ctx.share.registerRenderer`）
// 早在 commit 37134718 就进了 web 入口，但**清单从未声明 `share` 权限** ——
// 前端 `buildContext` 按权限注入（`web/src/plugin-runtime/context.ts`：
// `if (has('share')) ctx.share = svc.share`，`has = (p) => permissions.includes(p)`），
// 于是 `ctx.share === undefined`，而插件里 `const button = share ? <button/> : null`
// ⇒ **按钮静默消失（无任何报错）**，只能靠读代码才能定位。
//
// 与 git-fancy 的同类守护同理（`TestManifestPermissions`）：能力由插件**声明**，
// 漏声明 = 运行时该能力为 undefined，且失败是静默的 ⇒ 必须由测试挡住。
func TestManifestPermissions(t *testing.T) {
	data, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var m struct {
		Permissions []string `json:"permissions"`
		Web         struct {
			Entry string `json:"entry"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if m.Web.Entry == "" {
		t.Fatal("web.entry must be declared (the plugin ships a frontend runtime)")
	}

	perms := map[string]bool{}
	for _, p := range m.Permissions {
		perms[p] = true
	}
	// web 入口使用 ctx.share.create / ctx.share.registerRenderer（分享按钮 + 分享渲染器）。
	if !perms["share"] {
		t.Errorf("permissions must contain \"share\" (ctx.share.create + registerRenderer for the share button), got %v", m.Permissions)
	}
	// display_html 的渲染器经 ctx.contributes.register 声明（宿主 env 提供 contributes）。
	if !perms["ui.contribute"] && !perms["ui"] {
		t.Errorf("permissions must declare a UI capability (ctx.contributes.register / renderer), got %v", m.Permissions)
	}
}
