package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// TestGroupRoutes_PostGroupsCreates 回归 2026-10-22「接口有问题」：
// 文档（group-visualization.md §3.4）与前端 api.ts 都约定 `POST /api/groups` 建群，
// 但路由当初只注册了 `/api/groups/create` —— 按契约调用会掉进只允许 GET 的
// handleGroups，返回 405，面板/脚本无法建群。
//
// 判据：未登录时 POST /api/groups 必须是 401（进到建群处理器的鉴权），
// 而**不是** 405（掉进 GET 处理器）；GET 仍归列表处理器。
func TestGroupRoutes_PostGroupsCreates(t *testing.T) {
	wc := &WebChannel{sessions: map[string]sessionInfo{}}
	mux := http.NewServeMux()
	wc.registerGroupRoutes(mux)

	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/groups", strings.NewReader(`{"name":"朝廷"}`)))
	if post.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/groups 仍被路由到只允许 GET 的 handleGroups（405）")
	}
	if post.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/groups status = %d, body = %s", post.Code, post.Body.String())
	}

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/groups", nil))
	if get.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/groups status = %d, body = %s", get.Code, get.Body.String())
	}
}
