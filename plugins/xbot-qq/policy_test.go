package main

import "testing"

// TestPolicyShouldHandle 是阶段 1 的核心验收表：
//
//	① 白名单命中（精确）        → 允许处理
//	② 白名单未命中（非通配）    → 拒绝
//	③ 通配模式（"*"）           → 全部允许
//
// 外加两条安全默认：空白名单拒绝一切、空发送者拒绝。
func TestPolicyShouldHandle(t *testing.T) {
	tests := []struct {
		name   string
		allow  string
		sender string
		want   bool
	}{
		// ① 精确命中
		{"精确命中-QQ号", "10001,10002", "10001", true},
		{"精确命中-群标识", "group:20002", "group:20002", true},
		{"精确命中-带空白的名单", " 10001 , 10002 ", "10002", true},

		// ② 未命中
		{"未命中-不在名单", "10001,10002", "99999", false},
		{"未命中-只配了群通配", "group:*", "10001", false},
		{"未命中-前缀不同", "group:20002", "group:20003", false},

		// ③ 通配 → 全部允许
		{"通配-所有消息-QQ号", "*", "10001", true},
		{"通配-所有消息-群标识", "*", "group:20002", true},
		{"通配-所有消息-任意名字", "*", "anyone-at-all", true},
		{"通配-前缀匹配", "group:*", "group:12345", true},
		{"通配-单字符", "1000?", "10001", true},
		{"通配-单字符不匹配多位", "1000?", "100011", false},

		// 安全默认
		{"空白名单拒绝一切", "", "10001", false},
		{"空发送者即使全通配也拒绝", "*", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPolicy(ParsePatterns(tc.allow), ParsePatterns(""))
			if got := p.ShouldHandle(tc.sender); got != tc.want {
				t.Fatalf("allow=%q sender=%q: ShouldHandle = %v, want %v", tc.allow, tc.sender, got, tc.want)
			}
		})
	}
}

// TestPolicyIsAdmin 覆盖 admin 判定的两类验收：
//
//	④ admin 列表内 → admin=true
//	⑤ 列表外       → admin=false
func TestPolicyIsAdmin(t *testing.T) {
	tests := []struct {
		name   string
		admins string
		sender string
		want   bool
	}{
		// ④ 在名单内
		{"名单内-单值", "10001", "10001", true},
		{"名单内-多值", "10001,10002,10003", "10003", true},
		{"名单内-通配", "1000*", "10009", true},

		// ⑤ 不在名单内
		{"名单外", "10001,10002", "99999", false},
		{"空名单", "", "10001", false},
		{"名单外-前缀不同", "10001", "100011", false},
		{"空发送者", "10001", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPolicy(ParsePatterns("*"), ParsePatterns(tc.admins))
			if got := p.IsAdmin(tc.sender); got != tc.want {
				t.Fatalf("admins=%q sender=%q: IsAdmin = %v, want %v", tc.admins, tc.sender, got, tc.want)
			}
		})
	}
}

// TestPolicyAdmissionAndAdminAreIndependent 准入与权限是两件独立的事：
// 被白名单拒绝的管理员仍然被识别为管理员，被放行的普通成员不是管理员。
func TestPolicyAdmissionAndAdminAreIndependent(t *testing.T) {
	// 只放行群会话，管理员是 10001。注意 admin 名的 subject 是 QQ 号，
	// 群会话的 subject 是 "group:<群号>" —— 两者不是同一个命名空间。
	p := NewPolicy(ParsePatterns("group:*"), ParsePatterns("10001"))

	// 管理员，但私聊消息不在白名单 → 不处理，仍是管理员。
	if d := p.Decide("10001"); d.Handle || !d.Admin {
		t.Fatalf("admin outside allow list: Decide = %+v, want {Handle:false Admin:true}", d)
	}
	// 白名单内的群会话 → 处理，但不是管理员。
	if d := p.Decide("group:777"); !d.Handle || d.Admin {
		t.Fatalf("group member: Decide = %+v, want {Handle:true Admin:false}", d)
	}

	// 两者都满足：全放行 + 管理员名单命中。
	all := NewPolicy(ParsePatterns("*"), ParsePatterns("10001"))
	if d := all.Decide("10001"); !d.Handle || !d.Admin {
		t.Fatalf("allow-all admin: Decide = %+v, want {Handle:true Admin:true}", d)
	}
}
