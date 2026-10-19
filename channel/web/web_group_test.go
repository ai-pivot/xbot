package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"xbot/llm"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// TestHandleGroupMessages_SeedShapedData 验证群消息 API 走真实会话库 + keyset 分页，
// 返回 200 与正确的 items（kind / correlation_id / sender 信息全部还原）。
func TestHandleGroupMessages_SeedShapedData(t *testing.T) {
	sdb, err := sqlite.OpenSessionDB(filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatalf("OpenSessionDB: %v", err)
	}
	defer func() { _ = sdb.Close() }()
	svc := sqlite.NewSessionService(sdb)
	const tenantID = int64(3)
	base := time.Now().Add(-time.Hour)
	seed := []struct {
		kind, corr, sender, name, content string
	}{
		{"decision", "corr_1", "web-1", "首辅", "今日议题：B300 集群吞吐"},
		{"ask_user", "corr_1", "web-1", "首辅", "是否需要扩到 16 卡？"},
		{"answer", "corr_1", "hubu/main", "户部", "暂不扩容"},
	}
	for i, s := range seed {
		_, err := svc.AppendGroupMessage(tenantID, llm.ChatMessage{
			Role: "assistant", Content: s.content, Timestamp: base.Add(time.Duration(i) * time.Minute),
		}, sqlite.GroupMessageEnvelope{
			GroupID: "grp_demo", Kind: s.kind, CorrelationID: s.corr,
			SenderType: "agent", SenderID: s.sender, SenderName: s.name,
			To: []string{"agent:hubu/main"},
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	wc := &WebChannel{
		userCurrentSession: map[string]SessionSelector{"web-1": {Channel: "web", ChatID: "court-demo"}},
		callbacks: WebCallbacks{
			GroupMessages: func(memberAddr, groupID, channelName, chatID string, cursor int64, limit int) ([]protocol.GroupMessage, int64, bool, error) {
				if memberAddr != "web-1" || groupID != "grp_demo" || channelName != "web" || chatID != "court-demo" {
					t.Fatalf("unexpected args: %s %s %s %s", memberAddr, groupID, channelName, chatID)
				}
				rows, hasMore, next, err := svc.ListGroupMessages(tenantID, groupID, cursor, limit)
				if err != nil {
					return nil, 0, false, err
				}
				out := make([]protocol.GroupMessage, 0, len(rows))
				for _, r := range rows {
					out = append(out, protocol.GroupMessage{
						ID: r.ID, GroupID: r.GroupID, Kind: r.Kind, CorrelationID: r.CorrelationID,
						SenderType: r.SenderType, SenderID: r.SenderID, SenderName: r.SenderName,
						Role: r.Role, Content: r.Content, To: r.To, Payload: r.Payload, CreatedAt: r.CreatedAt,
					})
				}
				return out, next, hasMore, nil
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/groups/grp_demo/messages?limit=50", nil)
	req.SetPathValue("id", "grp_demo")
	req = req.WithContext(context.WithValue(req.Context(), senderIDKey, "web-1"))
	rr := httptest.NewRecorder()
	wc.handleGroupMessages(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Items []protocol.GroupMessage `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || len(resp.Data.Items) != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	first := resp.Data.Items[0]
	if first.Kind != "decision" || first.CorrelationID != "corr_1" ||
		first.SenderName != "首辅" || first.Content != "今日议题：B300 集群吞吐" {
		t.Fatalf("first item = %+v", first)
	}
	if len(first.To) != 1 || first.To[0] != "agent:hubu/main" {
		t.Fatalf("to = %+v", first.To)
	}
}
