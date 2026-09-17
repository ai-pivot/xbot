package tools

import (
	"slices"
	"testing"
)

// TestMemoryTools_ProviderExclusiveSets 锁定「工具只在正确时机注入」的不变量：
// 每个 memory provider 只能声明**自己的**工具，跨 provider 泄漏即红。
//
// 用户 2026-09-17：观察到 xbot 模式记忆里混进了 Letta 系工具（`archival_memory_insert`
// 等）。排查结论：声明接口 `RegisterMemoryTools`/`GetMemoryTools` 本身正确、注册点
// 唯一（agent.go 的 `GetMemoryTools(memoryProvider)`）——本测试把"集合 = 该 provider
// 声明的集合"固化为守护，任何新增工具或误挂都会在此失败。
func TestMemoryTools_ProviderExclusiveSets(t *testing.T) {
	want := map[string][]string{
		"flat":  {"memory_write", "memory_list"},
		"xbot":  {"memory_search", "memory_add", "memory_manage"},
		"letta": {"core_memory_append", "core_memory_replace", "rethink", "archival_memory_insert", "archival_memory_search", "recall_memory_search"},
	}

	for provider, expected := range want {
		tools := GetMemoryTools(provider)
		got := make([]string, 0, len(tools))
		for _, tl := range tools {
			if tl == nil {
				t.Fatalf("%s: nil tool in declared set", provider)
			}
			got = append(got, tl.Name())
		}
		slices.Sort(got)
		wantSorted := slices.Clone(expected)
		slices.Sort(wantSorted)
		if !slices.Equal(got, wantSorted) {
			t.Errorf("GetMemoryTools(%q) = %v, want %v", provider, got, wantSorted)
		}

		// 反向断言：绝不包含其它 provider 的任何工具名。
		for other, otherNames := range want {
			if other == provider {
				continue
			}
			for _, n := range otherNames {
				if slices.Contains(got, n) {
					t.Errorf("GetMemoryTools(%q) leaked %q from provider %q", provider, n, other)
				}
			}
		}
	}

	if n := len(GetMemoryTools("none")); n != 0 {
		t.Errorf("GetMemoryTools(\"none\") = %d tools, want 0 (no memory capability)", n)
	}
	if n := len(GetMemoryTools("不存在的provider")); n != 0 {
		t.Errorf("unknown provider must declare no tools, got %d", n)
	}
}
