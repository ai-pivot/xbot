package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// 守护：清单必须自带 web.i18n 文案表（宿主 ctx.i18n 的唯一数据源）。
// 曾发生（同类事故见 xbot.genui / xbot.ssh-runner）：表存在但被宿主漏传 ⇒
// 插件全走中文兜底（用户可见的「宿主英文、插件中文」）。
//
// 本测试同时守护三类**静默失效**（都只能靠读代码定位，跑起来不报错）：
//   1. 三个 view 的文案表缺失 / 三语 key 集合漂移（漏译 ⇒ 回退兜底）；
//   2. 能力权限漏声明（缺 "rpc" ⇒ ctx.rpc 不注入，趋势面板永远加载失败；
//      缺 "events" ⇒ turn.ended 自动刷新静默失效）；
//   3. i18n 插值写成单花括号 `{x}`（i18next 只认 `{{x}}`，单括号会被原样渲染给用户）。
func TestManifest_DeclaresI18nTable(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var m struct {
		ID          string   `json:"id"`
		Permissions []string `json:"permissions"`
		Web         struct {
			Entry      string                         `json:"entry"`
			Contributes []struct {
				Kind      string `json:"kind"`
				ID        string `json:"id"`
				Container string `json:"container"`
				Entry     string `json:"entry"`
			} `json:"contributes"`
			I18n map[string]map[string]string `json:"i18n"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if m.Web.Entry == "" {
		t.Fatal("web.entry missing: view 无法加载")
	}

	// ① i18n 表完整性
	for _, loc := range []string{"zh-CN", "en", "ja"} {
		table, ok := m.Web.I18n[loc]
		if !ok {
			t.Fatalf("web.i18n[%s] missing: 宿主切到该语言时插件只能回退兜底", loc)
		}
		if len(table) < 30 {
			t.Fatalf("web.i18n[%s] 仅 %d 条 —— 表不完整会让大量文案回退兜底", loc, len(table))
		}
		for key, text := range table {
			if strings.TrimSpace(text) == "" {
				t.Fatalf("web.i18n[%s][%s] 为空字符串（等价于漏译）", loc, key)
			}
			// 单花括号插值：i18next 26 只认 {{x}}，单括号会原样渲染给用户。
			if singleBraceRe.MatchString(text) {
				t.Fatalf("web.i18n[%s][%s]=%q 使用单花括号插值（i18next 只认 {{x}}，会原样显示给用户）", loc, key, text)
			}
		}
	}
	// 三语 key 集合必须一致（漏译即回退兜底文案）。
	ref := m.Web.I18n["en"]
	for _, loc := range []string{"zh-CN", "ja"} {
		for k := range ref {
			if _, ok := m.Web.I18n[loc][k]; !ok {
				t.Fatalf("web.i18n[%s] 缺 key %q（en 有、该语言没有 ⇒ 回退兜底）", loc, k)
			}
		}
		for k := range m.Web.I18n[loc] {
			if _, ok := ref[k]; !ok {
				t.Fatalf("web.i18n[en] 缺 key %q（%s 有、en 没有 ⇒ 英文界面回退中文兜底）", k, loc)
			}
		}
	}

	// ② 能力权限：缺一项就是静默失效（宿主按 permissions 注入 ctx 能力）。
	perms := make(map[string]bool, len(m.Permissions))
	for _, p := range m.Permissions {
		perms[p] = true
	}
	for _, required := range []string{"rpc", "events", "config"} {
		if !perms[required] {
			t.Fatalf("permissions 缺 %q —— 宿主不会注入该能力（趋势面板/自动刷新会静默失效）", required)
		}
	}

	// ③ 两个视图（徽章 + 趋势面板）都必须声明且共用 index.js 产物
	//    （单入口双视图：宿主按 view.id 解析 mod[view.id] 命名导出）。
	views := map[string]string{} // view id → container
	for _, c := range m.Web.Contributes {
		if c.Kind != "view" {
			continue
		}
		if c.Entry == "" {
			t.Fatalf("view %s 缺 entry", c.ID)
		}
		if c.Entry != m.Web.Entry {
			t.Fatalf("view %s entry=%q 与 web.entry=%q 不一致 —— 构建产物只有 index.js（需同步 release.yml 的 esbuild 块）", c.ID, c.Entry, m.Web.Entry)
		}
		views[c.ID] = c.Container
	}
	if _, ok := views["xbot.iteration-stats.badge"]; !ok {
		t.Fatal("缺 view xbot.iteration-stats.badge（状态栏实时徽章）")
	}
	if container, ok := views["xbot.iteration-stats.trend"]; !ok {
		t.Fatal("缺 view xbot.iteration-stats.trend（多粒度趋势面板）")
	} else if container != "right_sidebar" {
		t.Fatalf("趋势面板 container=%q，应为 right_sidebar", container)
	}
}

// 单花括号插值（排除 {{x}} 双括号与 CSS/代码里的花括号块）。
var singleBraceRe = regexp.MustCompile(`(^|[^{])\{[A-Za-z_][A-Za-z0-9_]*\}([^}]|$)`)
