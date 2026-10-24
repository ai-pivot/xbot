// seed-group-demo.go — 「Agent 通信指挥室」演示数据种子。
//
// 用法：
//
//	go run ./scripts/seed-group-demo.go -home ~/.xbot
//	go run ./scripts/seed-group-demo.go -home /tmp/xbot-team-demo -force
//
// 产物：
//   - 主库 groups / group_members：1 个群「团队晨会」，4 个成员（首辅 + 三部）
//   - 会话库 sessions/web/xx/<team-demo>.db：80 条跨天群消息，含
//     ask_user/answer/report/decision/action/text 六种 kind 与真实 correlation_id 线程
//
// 该脚本幂等：默认已存在同 id 群/消息则跳过；-force 先清空再重建。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

const (
	demoGroupID   = "grp_team_demo"
	demoGroupName = "团队晨会"
	demoChannel   = "web"
	demoChatID    = "team-demo"
	demoOwner     = "web-1"
)

type member struct {
	addr string
	name string
	role string
}

var members = []member{
	{"web-1", "首辅", "owner"},
	{"agent:hubu/main", "户部", "hubu"},
	{"agent:gongbu/main", "工部", "gongbu"},
	{"agent:libu/main", "吏部", "libu"},
}

func memberName(addr string) string {
	for _, m := range members {
		if m.addr == addr {
			return m.name
		}
	}
	return addr
}

func main() {
	home := flag.String("home", "", "xbot home directory (default: $XBOT_HOME or ~/.xbot)")
	force := flag.Bool("force", false, "delete existing demo messages and reseed")
	flag.Parse()

	h := *home
	if h == "" {
		h = os.Getenv("XBOT_HOME")
	}
	if h == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			fail("resolve home: %v", err)
		}
		h = filepath.Join(userHome, ".xbot")
	}
	mainDBPath := filepath.Join(h, "xbot.db")
	// 首次运行允许自动建库（sqlite.Open 会写入完整 schema 并跑到 v74）。
	db, err := sqlite.Open(mainDBPath)
	if err != nil {
		fail("open main db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if *force {
		if _, err := db.Conn().ExecContext(ctx, `DELETE FROM group_members WHERE group_id=?`, demoGroupID); err != nil {
			fail("clear members: %v", err)
		}
		if _, err := db.Conn().ExecContext(ctx, `DELETE FROM groups WHERE id=?`, demoGroupID); err != nil {
			fail("clear group: %v", err)
		}
	}

	groupSvc := sqlite.NewGroupService(db.Conn())
	addrs := make([]string, 0, len(members))
	for _, m := range members {
		addrs = append(addrs, m.addr)
	}
	g, err := groupSvc.GetGroup(ctx, demoGroupID)
	if err == sqlite.ErrGroupNotFound {
		g, err = groupSvc.CreateGroup(ctx, demoGroupID, demoGroupName, demoOwner, addrs)
		if err != nil {
			fail("create demo group: %v", err)
		}
	} else if err != nil {
		fail("read demo group: %v", err)
	}
	fmt.Printf("group %s (%s) owner=%s members=%d\n", g.ID, g.Name, g.Owner, g.MemberCount)

	tenantID, dbPath, err := ensureDemoTenant(db.Conn(), h)
	if err != nil {
		fail("ensure demo tenant: %v", err)
	}
	sdb, err := sqlite.OpenSessionDB(filepath.Join(h, dbPath))
	if err != nil {
		fail("open session db: %v", err)
	}
	defer func() { _ = sdb.Close() }()

	svc := sqlite.NewSessionService(sdb)
	if *force {
		if _, err := sdb.Conn().ExecContext(ctx, `DELETE FROM session_messages WHERE tenant_id=? AND group_id=?`, tenantID, demoGroupID); err != nil {
			fail("clear demo messages: %v", err)
		}
	} else {
		var n int
		if err := sdb.Conn().QueryRowContext(ctx,
			`SELECT COUNT(1) FROM session_messages WHERE tenant_id=? AND group_id=?`, tenantID, demoGroupID).Scan(&n); err == nil && n > 0 {
			fmt.Printf("demo messages already present (%d), skip (use -force to reseed)\n", n)
			return
		}
	}

	msgs := buildMessages()
	for i, m := range msgs {
		env := sqlite.GroupMessageEnvelope{
			GroupID:       demoGroupID,
			Kind:          m.kind,
			CorrelationID: m.corr,
			To:            m.to,
			Payload:       m.payload,
			SenderType:    m.senderType,
			SenderID:      m.sender,
			SenderName:    memberName(m.sender),
		}
		role := "assistant"
		if m.senderType == "user" {
			role = "user"
		}
		_, err := svc.AppendGroupMessage(tenantID, llm.ChatMessage{
			Role: role, Content: m.content, Timestamp: m.ts,
		}, env)
		if err != nil {
			fail("insert message %d: %v", i, err)
		}
	}
	fmt.Printf("seeded %d messages into %s (tenant %d)\n", len(msgs), filepath.Join(h, dbPath), tenantID)
	fmt.Printf("GET /api/groups/%s/messages?channel=%s&chat_id=%s\n", demoGroupID, demoChannel, demoChatID)
}

func ensureDemoTenant(conn *sql.DB, home string) (int64, string, error) {
	rel := sqlite.SessionDBRelPath(demoChannel, demoChatID)
	res, err := conn.Exec(`
		INSERT INTO tenants (channel, chat_id, db_path, migrated, last_active_at)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(channel, chat_id) DO UPDATE SET db_path=excluded.db_path, migrated=1`,
		demoChannel, demoChatID, rel)
	if err != nil {
		return 0, "", err
	}
	_ = res
	var tenantID int64
	var dbPath string
	if err := conn.QueryRow(`SELECT id, db_path FROM tenants WHERE channel=? AND chat_id=?`,
		demoChannel, demoChatID).Scan(&tenantID, &dbPath); err != nil {
		return 0, "", err
	}
	if dbPath == "" {
		dbPath = rel
		if _, err := conn.Exec(`UPDATE tenants SET db_path=? WHERE id=?`, dbPath, tenantID); err != nil {
			return 0, "", err
		}
	}
	_ = home
	return tenantID, dbPath, nil
}

type demoMsg struct {
	ts         time.Time
	sender     string
	senderType string
	kind       string
	corr       string
	content    string
	to         []string
	payload    map[string]any
}

func buildMessages() []demoMsg {
	day0 := time.Now().AddDate(0, 0, -3).Truncate(24 * time.Hour).Add(8 * time.Hour)
	msgs := make([]demoMsg, 0, 96)

	topics := []struct {
		corr    string
		title   string
		owner   string
		askee   string
		report  string
		metrics map[string]any
	}{
		{"corr_d1_gpu", "B300 集群今日吞吐", "agent:gongbu/main", "agent:gongbu/main",
			"工部回报：8 卡 B300 集群 2P1D 稳定，TPOT P50 18.2ms，TPM 142k。", map[string]any{"tpm": 142000, "tpot_ms": 18.2}},
		{"corr_d1_serve", "户部：昨日 token 用量", "agent:hubu/main", "agent:hubu/main",
			"户部回报：昨日总 token 8.4B，缓存命中率 63%，成本环比 -7%。", map[string]any{"tokens": "8.4B", "cache_hit": 0.63}},
		{"corr_d1_people", "吏部：值班与技能清单", "agent:libu/main", "agent:libu/main",
			"吏部回报：三名值守 agent 技能齐备，轮值表已更新。", map[string]any{"agents": 3}},
	}

	for d := 0; d < 3; d++ {
		day := day0.AddDate(0, 0, d)
		for ti, t := range topics {
			base := day.Add(time.Duration(9+ti) * time.Hour)
			corr := fmt.Sprintf("%s_%d", t.corr, d)
			// 首辅决策开题
			msgs = append(msgs, demoMsg{
				ts: base, sender: demoOwner, senderType: "user", kind: "decision", corr: corr,
				content: fmt.Sprintf("今日议题 %d：%s。请%s核实后回报。", ti+1, t.title, memberName(t.askee)),
				to:      []string{t.askee},
				payload: map[string]any{"decision": "approve", "topic": t.title, "priority": "P1"},
			})
			// ask_user（异步提问）
			msgs = append(msgs, demoMsg{
				ts: base.Add(4 * time.Minute), sender: demoOwner, senderType: "user", kind: "ask_user", corr: corr,
				content: fmt.Sprintf("请确认：%s 是否需要扩到 16 卡？", t.title),
				to:      []string{t.askee},
				payload: map[string]any{"question": "是否需要扩到 16 卡？", "options": []string{"扩", "不扩", "观望"}},
			})
			// answer
			msgs = append(msgs, demoMsg{
				ts: base.Add(11 * time.Minute), sender: t.askee, senderType: "agent", kind: "answer", corr: corr,
				content: "回禀：当前负载 72%，暂不扩容；若 TPM 连续两日 >160k 再申请。",
				to:      []string{demoOwner},
				payload: map[string]any{"answer": "暂不扩容", "confidence": 0.86},
			})
			// report
			msgs = append(msgs, demoMsg{
				ts: base.Add(26 * time.Minute), sender: t.askee, senderType: "agent", kind: "report", corr: corr,
				content: t.report,
				to:      []string{demoOwner},
				payload: map[string]any{"summary": t.report, "metrics": t.metrics},
			})
			// decision
			msgs = append(msgs, demoMsg{
				ts: base.Add(34 * time.Minute), sender: demoOwner, senderType: "user", kind: "decision", corr: corr,
				content: "准。继续按现网配置运行，明日晨会复盘。",
				to:      []string{t.askee},
				payload: map[string]any{"decision": "approved", "review_at": "tomorrow"},
			})
			// action / text 闲聊
			msgs = append(msgs, demoMsg{
				ts: base.Add(41 * time.Minute), sender: t.askee, senderType: "agent", kind: "action", corr: corr,
				content: fmt.Sprintf("%s已执行：更新看板并登记台账。", memberName(t.askee)),
				to:      []string{demoOwner},
				payload: map[string]any{"action": "update_dashboard", "status": "done"},
			})
			msgs = append(msgs, demoMsg{
				ts: base.Add(55 * time.Minute), sender: demoOwner, senderType: "user", kind: "text", corr: "",
				content: "收到，散会。",
			})
		}
		// 下午的跨部门协同（无 corr 的即时消息）
		noon := day.Add(14 * time.Hour)
		msgs = append(msgs, demoMsg{
			ts: noon, sender: "agent:gongbu/main", senderType: "agent", kind: "text", corr: "",
			content: "B300-3 节点网卡抖动，已切流；观察 30 分钟。",
			to:      []string{"agent:hubu/main", demoOwner},
			payload: map[string]any{"severity": "warn"},
		})
		msgs = append(msgs, demoMsg{
			ts: noon.Add(9 * time.Minute), sender: "agent:hubu/main", senderType: "agent", kind: "text", corr: "",
			content: "收到，已把流量切到 B300-4，业务无感。",
			to:      []string{"agent:gongbu/main"},
		})
	}
	return msgs
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seed-group-demo: "+format+"\n", args...)
	os.Exit(1)
}
