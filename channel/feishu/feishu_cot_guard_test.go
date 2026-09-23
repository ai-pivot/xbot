package feishu

import (
	"os"
	"strings"
	"testing"
)

// ⛔ 守护：测试代码**不得直接构造 `feishuCoT`** —— 必须走 `newFakeCoT`
// （它设 `draining = true`，禁用 `emit` 隐式启动的异步 drain goroutine）。
//
// 为什么（2026-09-23 master CI 失败根治）：`feishuCoT.emit` 会 `go c.drain()`
// 启动异步写线程；测试若直接 `newFeishuCoT(...)` 再调 `flushNow()`，两个写线程
// 争抢同一 `pending` 队列 —— drain 先取批时把失败消费掉（重试 3 次后置 broken +
// pending=nil），`flushNow` 只看到空队列/已 broken 便返回 nil ⇒ 断言随时序变化
// （Windows 调度下必现 `expected create rejection`；Linux 通常 flushNow 抢到；
// **`-race` 抓不到** —— 这不是数据竞争，是"谁先取批"的逻辑时序竞态）。
//
// 本守护有判别力：修复前 `feishu_cot_test.go` 有 **2** 处直接构造（newFakeCoT 内
// 的 1 处 + `TestFeishuCoT_CreateRejectedSurfacesPlatformMsg` 内的 1 处）⇒ 红；
// 修复后只允许 newFakeCoT 内的 1 处。
func TestGuard_NoDirectFeishuCoTConstructionInTests(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	// feishu_cot_test.go 内允许恰好 1 处：newFakeCoT helper 自身的构造点。
	allowed := map[string]int{"feishu_cot_test.go": 1}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		// 检查器自身：实现里必然含被检查的字面量（"newFeishuCoT("），自指会误报。
		if name == guardSelfFile {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		n := countCodeSubstring(string(data), "newFeishuCoT(")
		if want, ok := allowed[name]; ok {
			if n != want {
				t.Errorf("%s: %d 处非注释 newFeishuCoT(...)，期望 %d（仅 newFakeCoT 内）", name, n, want)
			}
			continue
		}
		if n > 0 {
			t.Errorf("%s 直接构造 feishuCoT ⇒ 必须用 newFakeCoT（禁用异步 drainer，否则测试与 drain goroutine 争抢 pending 队列 ⇒ flaky；见该 helper 注释）", name)
		}
	}
}

// guardSelfFile 是本守护自身的文件名（自指豁免）。
const guardSelfFile = "feishu_cot_guard_test.go"

// countCodeSubstring 统计**非注释行**里 sub 的出现次数（跳过 `//` 与 `/* */`，
// 避免注释里的示例名误报）。
func countCodeSubstring(src, sub string) int {
	n := 0
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			inBlock = !strings.Contains(trimmed, "*/")
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		n += strings.Count(line, sub)
	}
	return n
}
