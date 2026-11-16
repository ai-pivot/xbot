package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 生产窗口（2026-10-28T05:32Z 起 2.7 天）内，"Failed to restore interactive agent"
// 共 250 条，错误串唯一值只有一个：
//
//	cannot restore sub-agent: role definition explore not found in …/agents
//	(the role file was deleted or renamed after spawn)
//
// 根因：explore 是**内置（embed）role**，本就不在 agents 目录里；spawn 侧一直走
// tools.GetSubAgentRole 的 embed fallback，恢复侧却只查磁盘。于是每次恢复内置 role
// 的子树都整棵跳过并打一条 warning。
//
// 下面三条把「回落 embed」与「磁盘优先」「未知 role 仍必须失败」同时钉死。

func writeRoleFile(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nYou are a test role.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLookupRoleDefinition_FallsBackToEmbeddedRole(t *testing.T) {
	dir := t.TempDir() // 空 agents 目录 = 生产形态（agents 下只有 court/ proj/ 与两个顶层 md）
	a := &Agent{agentsDir: dir}

	role, err := a.lookupRoleDefinition("explore")
	if err != nil {
		t.Fatalf("内置 role explore 恢复失败（生产 250 条 warning 的根因）：%v", err)
	}
	if role.Name != "explore" {
		t.Fatalf("拿到的 role 名 = %q，want explore", role.Name)
	}
	if strings.TrimSpace(role.SystemPrompt) == "" {
		t.Fatal("从 embed 解析出的 role 没有 SystemPrompt —— 等于用空 prompt 的假角色顶上")
	}
}

func TestLookupRoleDefinition_DiskBeatsEmbedded(t *testing.T) {
	dir := t.TempDir()
	writeRoleFile(t, dir, "explore", "disk-version-marker")

	a := &Agent{agentsDir: dir}
	role, err := a.lookupRoleDefinition("explore")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if role.Description != "disk-version-marker" {
		t.Fatalf("磁盘定义必须覆盖 embed（got description=%q）", role.Description)
	}
}

func TestLookupRoleDefinition_UnknownRoleStillFails(t *testing.T) {
	a := &Agent{agentsDir: t.TempDir()}

	_, err := a.lookupRoleDefinition("zz_no_such_role_20261031")
	if err == nil {
		t.Fatal("未知 role 必须返回错误 —— 不能用空 prompt 的假角色顶上")
	}
	if _, ok := err.(*restoreRoleMissingError); !ok {
		t.Fatalf("错误类型 = %T，want *restoreRoleMissingError", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("错误串丢了定位信息: %v", err)
	}
}
