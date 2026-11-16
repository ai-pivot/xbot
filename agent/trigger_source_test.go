package agent

import "testing"

// 本文件是**实现侧**单测：引用 agent/trigger_source.go 的常量与小函数。
//
// 与 cron_turn_no_notify_test.go 分文件的原因：那份只用来打 before-red，必须能在
// **未改动的树**上编译（只能引用既有符号、只用字面量）；本文件引用实现符号，
// 在未改树上不可编译，所以 before-red 期间要把它临时移出包目录
// （见 evidence 的 before-red.log 头部说明）。

// TestCronTurnTriggerSourceWireFormat 保证测试里用的字面量与实现常量一致。
func TestCronTurnTriggerSourceWireFormat(t *testing.T) {
	if testTriggerSourceKey != triggerSourceMetadataKey {
		t.Fatalf("test literal %q != triggerSourceMetadataKey %q", testTriggerSourceKey, triggerSourceMetadataKey)
	}
	if testTriggerSourceCron != triggerSourceCron {
		t.Fatalf("test literal %q != triggerSourceCron %q", testTriggerSourceCron, triggerSourceCron)
	}
}

// TestBgSectionsTriggerSource 混合批次不误标（纯函数层）：一次突发 = 一条 user
// 消息 = 一个 turn，只有**整条**都来自 cron 才能标 cron；混批必须不标 ——
// 混批那一轮里可能夹着人要看的真实结果，按 cron 抑制会吞掉它。
func TestBgSectionsTriggerSource(t *testing.T) {
	cases := []struct {
		name             string
		cron, other      int
		wantTriggerValue string
	}{
		{"single cron", 1, 0, testTriggerSourceCron},
		{"two cron", 2, 0, testTriggerSourceCron},
		{"cron + bg task (mixed)", 1, 1, ""},
		{"bg task + cron (mixed)", 1, 2, ""},
		{"no cron", 0, 3, ""},
		{"empty", 0, 0, ""},
	}
	for _, c := range cases {
		if got := bgSectionsTriggerSource(c.cron, c.other); got != c.wantTriggerValue {
			t.Errorf("%s: bgSectionsTriggerSource(%d,%d) = %q, want %q",
				c.name, c.cron, c.other, got, c.wantTriggerValue)
		}
	}
}

// TestShouldNotifyParent_FailOpen 判定口径（纯函数层）：cron turn 只有在**没有**
// 真实用户输入并入时才抑制；一旦有人主动给它发了消息（pendingUserMsg 被 drain
// 注入 ⇒ userInputJoined=true），必须照常通知，否则用户派单没有回执（fail-open）。
func TestShouldNotifyParent_FailOpen(t *testing.T) {
	userCfg := &RunConfig{}
	cronCfg := &RunConfig{TriggerSource: triggerSourceCron}
	if !shouldNotifyParent(userCfg, false) {
		t.Error("user turn must notify parent")
	}
	if shouldNotifyParent(cronCfg, false) {
		t.Error("pure cron turn must NOT notify parent")
	}
	if !shouldNotifyParent(cronCfg, true) {
		t.Error("cron turn with real user input joined must notify parent (fail-open)")
	}
	if !shouldNotifyParent(nil, false) {
		t.Error("nil cfg must notify parent (nil-safe)")
	}
}
