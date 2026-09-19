package main

import (
	"encoding/json"
	"os"
	"testing"
)

// 守护：清单必须自带 web.i18n 文案表（宿主 ctx.i18n 的唯一数据源）。
//
// 本插件此前借用宿主的 `window.__xbot_i18n__`（key `plugins.gitFancy.*`）——
// 文案写进了宿主 `web/src/i18n/*.ts`，插件无法独立分发，且宿主切到 en 时
// 必须由宿主语言包提供英文。现改为**文案随插件清单走**（见
// `web/src/plugin-runtime/i18n.ts` 的契约）。
//
// ⚠️ 本目录是独立 Go module —— 在该目录内 `go test .` 运行。
func TestManifest_DeclaresI18nTable(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var m struct {
		Web struct {
			Entry string                       `json:"entry"`
			I18n  map[string]map[string]string `json:"i18n"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if m.Web.Entry == "" {
		t.Fatal("web.entry missing: view 无法加载")
	}
	// 下限 = 当前实际条数（面板 loading/notRepo/刷新/diff/commit 列表 + commit 详情视图
	// + 会话身份未就绪 / rpc 未注入 两条错误文案 + contributes.configuration 的
	// label/description 4 条 —— 设置页经插件表解析这些 key，见 SettingsPlugins.resolvePluginText）。
	// 只增不减：删 key 属于功能回退，必须显式改这个下限并说明原因。
	const minKeys = 23
	for _, loc := range []string{"zh-CN", "en", "ja"} {
		table, ok := m.Web.I18n[loc]
		if !ok {
			t.Fatalf("web.i18n[%s] missing: 宿主切到该语言时插件只能回退中文兜底", loc)
		}
		if len(table) < minKeys {
			t.Fatalf("web.i18n[%s] 仅 %d 条（下限 %d）—— 表不完整会让文案回退兜底", loc, len(table), minKeys)
		}
	}
	// 三语 key 集合必须一致（漏译即回退中文兜底）。
	ref := m.Web.I18n["en"]
	for _, loc := range []string{"zh-CN", "ja"} {
		for k := range ref {
			if _, ok := m.Web.I18n[loc][k]; !ok {
				t.Fatalf("web.i18n[%s] 缺 key %q（en 有、该语言没有 ⇒ 回退兜底）", loc, k)
			}
		}
		for k := range m.Web.I18n[loc] {
			if _, ok := ref[k]; !ok {
				t.Fatalf("web.i18n[%s] 多出 key %q（en 没有 ⇒ 其他语言永远读不到）", loc, k)
			}
		}
	}
}
