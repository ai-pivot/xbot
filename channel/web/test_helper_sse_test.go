package web

import (
	"testing"
	"time"
)

// waitForRouteEventType 等待某事件类型到达指定 route（原定义在
// web_askuser_resolved_test.go）。旧 AskUser 已删除，但 SSE 通用测试仍用它
// 断言事件已落盘/可回放。
func waitForRouteEventType(t *testing.T, wc *WebChannel, sel SessionSelector, msgType string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range wc.replaySSEEvents(sel, 0) {
			if ev.Type == msgType {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event type %q never reached route %s\x00%s", msgType, sel.Channel, sel.ChatID)
}
