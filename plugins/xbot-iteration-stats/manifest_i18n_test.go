package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// 守护：清单必须自带 web.i18n 文案表（宿主 ctx.i18n / resolvePluginText 的唯一数据源）。
//
// 本插件本轮（2026-09-19）把清单里**所有用户可见文本**改成了文案表的 key：
//
//	name → manifest.name · description → manifest.description
//	view xbot.iteration-stats.badge.title → view.badge.title
//	contributes.configuration.title → config.title
//	properties.showTTFT.label / .description → config.showTTFT.label / .description
//
// 因此这里守三类**静默失效**（都只能靠读代码定位，跑起来不报错）：
//  1. 表缺失 / 三语 key 集合漂移（漏译 ⇒ 回退兜底）；
//  2. **清单文本声称是 key 却不在表里** ⇒ 宿主解析不到，用户直接看到裸 key
//     （2026-09-19 真实事故：name 改成 key 但 helper 未接线，界面出现 `manifest.name`）；
//  3. i18n 插值写成单花括号 `{x}`（i18next 只认 `{{x}}`，单括号会原样渲染给用户）。
func TestManifest_DeclaresI18nTable(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var m struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
		Web         struct {
			Entry       string `json:"entry"`
			Contributes []struct {
				Kind      string `json:"kind"`
				ID        string `json:"id"`
				Container string `json:"container"`
				Title     string `json:"title"`
				Entry     string `json:"entry"`
			} `json:"contributes"`
			I18n map[string]map[string]string `json:"i18n"`
		} `json:"web"`
		Contributes struct {
			Configuration struct {
				Title      string `json:"title"`
				Properties map[string]struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"configuration"`
		} `json:"contributes"`
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
		if len(table) < 6 {
			t.Fatalf("web.i18n[%s] 仅 %d 条（下限 6）—— 表不完整会让文案回退兜底", loc, len(table))
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

	// ② 清单里"看起来是 key"的可见文本必须在表里 —— 否则宿主解析不到，用户看到裸 key。
	//    裸字面量（不含点、或含空格/CJK）按设计原样透传，不在此约束内。
	keyShape := regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)+$`)
	check := func(field, value string) {
		if value == "" || !keyShape.MatchString(value) {
			return // 字面量：设计上允许（向后兼容）
		}
		for _, loc := range []string{"zh-CN", "en", "ja"} {
			if _, ok := m.Web.I18n[loc][value]; !ok {
				t.Fatalf("%s=%q 看起来是 key，但 web.i18n[%s] 里没有 —— 用户会看到裸 key", field, value, loc)
			}
		}
	}
	check("name", m.Name)
	check("description", m.Description)
	for _, c := range m.Web.Contributes {
		if c.Kind == "view" {
			check("view."+c.ID+".title", c.Title)
		}
	}
	check("contributes.configuration.title", m.Contributes.Configuration.Title)
	for k, p := range m.Contributes.Configuration.Properties {
		check("properties."+k+".label", p.Label)
		check("properties."+k+".description", p.Description)
	}

	// ③ 能力权限：缺一项就是静默失效（宿主按 permissions 注入 ctx 能力）。
	//    config：徽章读 showTTFT 配置。
	perms := make(map[string]bool, len(m.Permissions))
	for _, p := range m.Permissions {
		perms[p] = true
	}
	if !perms["config"] {
		t.Fatal(`permissions 缺 "config" —— 宿主不会注入 ctx.config（showTTFT 读不到）`)
	}

	// ④ 徽章视图必须声明，且 entry 与 web.entry 一致（单入口产物 index.js）。
	views := map[string]string{}
	for _, c := range m.Web.Contributes {
		if c.Kind != "view" {
			continue
		}
		if c.Entry == "" {
			t.Fatalf("view %s 缺 entry", c.ID)
		}
		if c.Entry != m.Web.Entry {
			t.Fatalf("view %s entry=%q 与 web.entry=%q 不一致（构建产物只有 index.js）", c.ID, c.Entry, m.Web.Entry)
		}
		views[c.ID] = c.Container
	}
	container, ok := views["xbot.iteration-stats.badge"]
	if !ok {
		t.Fatal("缺 view xbot.iteration-stats.badge（状态栏实时徽章）")
	}
	if container != "status_bar_right" {
		t.Fatalf("徽章 container=%q，应为 status_bar_right", container)
	}
}

// 单花括号插值（i18next 只认 {{x}}）。
var singleBraceRe = regexp.MustCompile(`(^|[^{])\{[a-zA-Z_][a-zA-Z0-9_.]*\}([^}]|$)`)
