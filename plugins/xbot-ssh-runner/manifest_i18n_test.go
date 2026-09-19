package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// 守护：清单必须自带 web.i18n 文案表（宿主 ctx.i18n / resolvePluginText 的唯一数据源）。
// 曾发生：表存在但被宿主漏传 ⇒ 插件全走中文兜底（用户可见的「宿主英文插件中文」）。
//
// 本轮（2026-09-19）起，清单里**用户可见文本**（name / description / view title）改为
// 文案表的 key ⇒ 额外守护「声称是 key 的文本必须在三语表里」，否则宿主解析不到、
// 用户直接看到裸 key（同日真实事故：name 改成 key 但 helper 未接线，界面出现 `manifest.name`）。
func TestManifest_DeclaresI18nTable(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var m struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Web         struct {
			Entry       string `json:"entry"`
			Contributes []struct {
				Kind  string `json:"kind"`
				ID    string `json:"id"`
				Title string `json:"title"`
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
	for _, loc := range []string{"zh-CN", "en", "ja"} {
		table, ok := m.Web.I18n[loc]
		if !ok {
			t.Fatalf("web.i18n[%s] missing: 宿主切到该语言时插件只能回退兜底", loc)
		}
		if len(table) < 50 {
			t.Fatalf("web.i18n[%s] 仅 %d 条 —— 表不完整会让大量文案回退兜底", loc, len(table))
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

	// 清单里"看起来是 key"的可见文本必须在三语表里（字面量按设计原样透传，不在此约束）。
	keyShape := regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)+$`)
	check := func(field, value string) {
		if value == "" || !keyShape.MatchString(value) {
			return
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
	// 设置页里直接显示给用户的配置项文案（label / description）同样必须是表里的 key ——
	// 否则宿主英文时用户看到中文（或裸 key）。
	for k, p := range m.Contributes.Configuration.Properties {
		check("properties."+k+".label", p.Label)
		check("properties."+k+".description", p.Description)
	}
}
