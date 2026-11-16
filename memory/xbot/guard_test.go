package xbot

// guard_test.go — 记忆写入闸门的回归守卫（2026-11-11）。
//
// 背景：生产记忆库里出现 #112343 —— importance 落库为 24440.0、正文混着
// "</think><tool_call>" 与被截断的乱码碎片。ListMemories 用 OrderImportance
// 排序，于是这一行永久霸占 memory_manage list 第一位，正常记忆被挤出前 50 条，
// 用户看到的现象就是「记忆无法正常更新」。
//
// 三处根因：
//  1. 全链路没有 clamp importance（24440 可写入）；
//  2. validateMemoryContent 不认协议控制标签（被截断的流式输出照收）；
//  3. AddMemory / UpdateMemory 根本不调用 validateMemoryContent —— 前者是公开
//     API，后者是 memory_manage update 的直接入口，等于留了两条绕过闸门的写路径。

import (
	"context"
	"math"
	"testing"
)

func TestClampImportance(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0, 0.5},       // 未设置 → 默认
		{-3, 0.5},      // 负数 → 默认
		{0.35, 0.35},   // 正常区间原样
		{1, 1},         // 上界
		{1.0000001, 1}, // 超界钳到 1
		{24440, 1},     // 线上实证值
	}
	for _, c := range cases {
		if got := clampImportance(c.in); got != c.want {
			t.Errorf("clampImportance(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := clampImportance(bad); got != 0.5 {
			t.Errorf("clampImportance(%v) = %v, want 0.5 (non-finite)", bad, got)
		}
	}
}

func TestValidateRejectsProtocolControlTags(t *testing.T) {
	bad := []string{
		"某条记忆 </think><tool_call>importance",
		"normal text <|im_start|>system",
		"x <system>y</system>",
		"a [INST] b [/INST]",
	}
	for _, b := range bad {
		if err := validateMemoryContent(b); err == nil {
			t.Errorf("must reject content with protocol control tag: %q", b)
		}
	}
	if err := validateMemoryContent("正常的一条中文记忆，没有任何协议标记。"); err != nil {
		t.Errorf("legit content must still pass, got %v", err)
	}
}

// TestAddAndUpdateMemoryEnforceGates — AddMemory 是公开 API、UpdateMemory 是
// memory_manage update 的直接入口；修复前两者都直接落库，既不校验内容也不钳制
// importance。
func TestAddAndUpdateMemoryEnforceGates(t *testing.T) {
	m, _ := newTestMemory(t)
	ctx := context.Background()

	// ① AddMemory 拒绝协议控制标签
	if _, err := m.AddMemory(ctx, LongTermMemory{Type: "fact", Content: "bad </think> tail"}); err == nil {
		t.Fatal("AddMemory must reject protocol control tags")
	}

	// ② AddMemory 钳制 importance
	id, err := m.AddMemory(ctx, LongTermMemory{
		Type: "fact", Content: "importance 越界应在写入时被钳制", Importance: 9999,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	row, err := m.store.GetAgentLongTermMemory(ctx, m.scopeArg(), m.tenantID, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Importance > 1 {
		t.Errorf("importance must be clamped on write, got %v", row.Importance)
	}

	// ③ UpdateMemory 同样过闸门
	if err := m.UpdateMemory(ctx, id, "bad <|im_start|>payload", "", "", 0.5); err == nil {
		t.Fatal("UpdateMemory must reject protocol control tags")
	}
	if err := m.UpdateMemory(ctx, id, "更新后的正常内容", "", "", 8888); err != nil {
		t.Fatalf("update: %v", err)
	}
	row2, err := m.store.GetAgentLongTermMemory(ctx, m.scopeArg(), m.tenantID, id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if row2.Importance > 1 {
		t.Errorf("importance must be clamped on update, got %v", row2.Importance)
	}
}
