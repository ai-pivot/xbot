package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xbot/bus"
	ch "xbot/channel"
	"xbot/protocol"
)

// =============================================================================
// POST /api/regions / POST /api/iteration_detail 的 REST 契约
// （docs/plan-history-fold-windowing.md §3.2/§3.3，T9：缺参 400 / 未命中 404 /
//   属主隔离 404 / 未接线 501 / 响应信封与 /api/history 一致）
// =============================================================================

func postRegions(t *testing.T, wc *WebChannel, body string) (*httptest.ResponseRecorder, testAPIEnvelope, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	wc.handleRegions(recorder, authedAPIRequest(http.MethodPost, "/api/regions", []byte(body)))
	envelope, data := decodeAPIResponse(t, recorder)
	return recorder, envelope, data
}

func postIterationDetail(t *testing.T, wc *WebChannel, body string) (*httptest.ResponseRecorder, testAPIEnvelope, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	wc.handleIterationDetail(recorder, authedAPIRequest(http.MethodPost, "/api/iteration_detail", []byte(body)))
	envelope, data := decodeAPIResponse(t, recorder)
	return recorder, envelope, data
}

// 缺参（turn_id / before_iteration 缺失或非法）⇒ 400，且**不得**触达回调。
func TestRESTRegions_MissingParamsIs400(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	called := false
	wc.SetCallbacks(WebCallbacks{
		HistoryRegions: func(string, SessionSelector, uint64, int, int) ([]protocol.HistoryIteration, int, error) {
			called = true
			return nil, 0, nil
		},
	})
	for _, body := range []string{
		`{"channel":"web","chat_id":"web-1","before_iteration":52}`,               // 缺 turn_id
		`{"channel":"web","chat_id":"web-1","turn_id":47}`,                        // 缺 before_iteration
		`{"channel":"web","chat_id":"web-1","turn_id":47,"before_iteration":0}`,   // before_iteration 非法
		`{"channel":"web","chat_id":"web-1","turn_id":0,"before_iteration":52}`,   // turn_id 非法
		`{"channel":"web","chat_id":"web-1","before_iteration":52,"turn_id":"x"}`, // 类型错
	} {
		recorder, _, _ := postRegions(t, wc, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s ⇒ status %d, want 400", body, recorder.Code)
		}
	}
	if called {
		t.Fatal("缺参请求不得触达 HistoryRegions 回调")
	}
}

// 正常路径：回调入参透传（turn_id / before_iteration / region_limit）+ 响应信封
// {iterations, regions_before}（与 /api/history 同款 writeJSON 信封）。
func TestRESTRegions_HappyPathContract(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	wc.SetCallbacks(WebCallbacks{
		HistoryRegions: func(senderID string, sel SessionSelector, turnID uint64, beforeIter, regionLimit int) ([]protocol.HistoryIteration, int, error) {
			if senderID != "web-1" || sel.Channel != "web" || sel.ChatID != "web-1" {
				t.Fatalf("wrong selector: sender=%q sel=%#v", senderID, sel)
			}
			if turnID != 47 || beforeIter != 52 || regionLimit != 25 {
				t.Fatalf("params = (turn %d, before %d, limit %d), want (47, 52, 25)", turnID, beforeIter, regionLimit)
			}
			return []protocol.HistoryIteration{
				{Iteration: 50, Content: "a", ToolsFolded: true},
				{Iteration: 51, Content: "b", ToolsFolded: true},
			}, 3, nil
		},
	})

	recorder, envelope, data := postRegions(t, wc, `{"channel":"web","chat_id":"web-1","turn_id":47,"before_iteration":52,"region_limit":25}`)
	if recorder.Code != http.StatusOK || !envelope.OK || envelope.Error != nil {
		t.Fatalf("status=%d envelope=%+v", recorder.Code, envelope)
	}
	iterations, ok := data["iterations"].([]any)
	if !ok || len(iterations) != 2 {
		t.Fatalf("iterations = %#v, want 2 条", data["iterations"])
	}
	first := iterations[0].(map[string]any)
	if first["iteration"] != float64(50) || first["tools_folded"] != true {
		t.Fatalf("iteration[0] = %#v, want iteration=50 tools_folded=true", first)
	}
	if data["regions_before"] != float64(3) {
		t.Fatalf("regions_before = %#v, want 3", data["regions_before"])
	}
}

// 属主隔离：不可访问 / 未知会话 ⇒ 404（与 /api/history 同一条 resolveAPISession 防线），
// 且不得触达回调。
func TestRESTRegions_UnknownSessionIs404AndDoesNotReachCallback(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	called := false
	wc.SetCallbacks(WebCallbacks{
		SessionExists: func(channel, chatID string) bool { return false },
		HistoryRegions: func(string, SessionSelector, uint64, int, int) ([]protocol.HistoryIteration, int, error) {
			called = true
			return nil, 0, nil
		},
	})
	recorder, _, _ := postRegions(t, wc, `{"channel":"web","chat_id":"ghost-chat","turn_id":47,"before_iteration":52}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知会话 ⇒ status %d, want 404（属主隔离）", recorder.Code)
	}
	if called {
		t.Fatal("属主校验失败不得触达回调")
	}
}

// 未接线（嵌入场景）⇒ 501，绝不返回「空但成功」的假结果（那会让前端以为该 turn 到顶）。
func TestRESTRegions_CallbackNotWiredIs501(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	recorder, _, _ := postRegions(t, wc, `{"channel":"web","chat_id":"web-1","turn_id":47,"before_iteration":52}`)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("未接线 ⇒ status %d, want 501", recorder.Code)
	}
}

// iteration_detail：命中返回完整迭代；未命中 404（与属主校验失败同状态码，防探测）；
// 缺参 400；未接线 501。
func TestRESTIterationDetail_Contract(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(wc, SessionSelector{Channel: "web", ChatID: "web-1"})
	hit := false
	wc.SetCallbacks(WebCallbacks{
		IterationDetail: func(senderID string, sel SessionSelector, turnID uint64, iteration int) (protocol.HistoryIteration, bool, error) {
			if senderID != "web-1" || sel.ChatID != "web-1" || turnID != 47 || iteration != 52 {
				t.Fatalf("wrong args: sender=%q sel=%#v turn=%d iter=%d", senderID, sel, turnID, iteration)
			}
			if !hit {
				return protocol.HistoryIteration{}, false, nil
			}
			return ch.HistoryIteration{
				Iteration: 52,
				Content:   "full",
				Tools: []protocol.ToolProgress{{
					Name: "Shell", Label: "Shell", Status: "done", Iteration: 52,
					Summary: "s", Args: `{"cmd":"ls"}`, Detail: "output",
				}},
			}, true, nil
		},
	})

	// 未命中 ⇒ 404。
	recorder, envelope, _ := postIterationDetail(t, wc, `{"channel":"web","chat_id":"web-1","turn_id":47,"iteration":52}`)
	if recorder.Code != http.StatusNotFound || envelope.OK {
		t.Fatalf("未命中 ⇒ status %d envelope=%+v, want 404", recorder.Code, envelope)
	}

	// 命中 ⇒ 200 + {iteration: 完整 HistoryIteration}。
	hit = true
	recorder, envelope, data := postIterationDetail(t, wc, `{"channel":"web","chat_id":"web-1","turn_id":47,"iteration":52}`)
	if recorder.Code != http.StatusOK || !envelope.OK {
		t.Fatalf("命中 ⇒ status %d envelope=%+v", recorder.Code, envelope)
	}
	it, ok := data["iteration"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want {iteration: {...}}", data)
	}
	if it["iteration"] != float64(52) || it["content"] != "full" {
		t.Fatalf("iteration payload = %#v", it)
	}
	tools := it["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["summary"] != "s" || tool["detail"] != "output" || tool["args"] != `{"cmd":"ls"}` {
		t.Fatalf("详情端点必须返回**完整**工具字段：%#v", tool)
	}

	// 缺参 ⇒ 400。
	for _, body := range []string{
		`{"channel":"web","chat_id":"web-1","iteration":52}`,
		`{"channel":"web","chat_id":"web-1","turn_id":47}`,
	} {
		recorder, _, _ := postIterationDetail(t, wc, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s ⇒ status %d, want 400", body, recorder.Code)
		}
	}

	// 未接线 ⇒ 501。
	empty := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	setTestCurrentSession(empty, SessionSelector{Channel: "web", ChatID: "web-1"})
	recorder, _, _ = postIterationDetail(t, empty, `{"channel":"web","chat_id":"web-1","turn_id":47,"iteration":52}`)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("未接线 ⇒ status %d, want 501", recorder.Code)
	}
}

// 路由级：两个新端点必须注册在 authenticatedPOST 上（未认证 ⇒ 401），
// 与 /api/history 同一条防线。
func TestRESTFoldEndpointsRequireAuth(t *testing.T) {
	wc := NewWebChannel(WebChannelConfig{}, bus.NewMessageBus())
	mux := wc.newServeMux()
	for _, path := range []string{"/api/regions", "/api/iteration_detail"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未认证 ⇒ status %d, want 401（必须走 authenticatedPOST）", path, rec.Code)
		}
	}
}

var _ = json.Marshal
