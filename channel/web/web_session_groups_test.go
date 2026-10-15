package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func groupRequest(wc *WebChannel, sender, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/session-groups", strings.NewReader(body))
	r = r.WithContext(contextWithSenderID(r.Context(), sender))
	w := httptest.NewRecorder()
	wc.handleSessionGroups(w, r)
	return w
}
func groupState(t *testing.T, w *httptest.ResponseRecorder) sessionGroupsState {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var state sessionGroupsState
	decodeAPIData(t, w.Body, &state)
	return state
}
func groupJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestSessionGroupsCRUD(t *testing.T) {
	db := newTestDB(t)
	wc := &WebChannel{db: db}
	empty := groupState(t, groupRequest(wc, "web-1", ""))
	if empty.Revision != 0 || empty.Groups == nil || empty.Memberships == nil {
		t.Fatalf("bad empty state: %+v", empty)
	}
	a := groupState(t, groupRequest(wc, "web-1", `{"action":"create","name":"A","defaultWorkDir":"/repo"}`))
	id := a.Groups[0].ID
	b := groupState(t, groupRequest(wc, "web-1", `{"action":"convert","name":"B","sessions":[{"channel":"web","chatID":"same"},{"channel":"cli","chatID":"same"}]}`))
	bid := b.Groups[1].ID
	if b.Memberships["web:same"] != bid || b.Memberships["cli:same"] != bid {
		t.Fatal("composite membership lost")
	}
	move := map[string]any{"action": "move", "groupId": id, "sessions": []sessionGroupSelector{{"web", "same"}}}
	c := groupState(t, groupRequest(wc, "web-1", groupJSON(move)))
	if c.Memberships["web:same"] != id || c.Memberships["cli:same"] != bid {
		t.Fatal("move affected other channel")
	}
	groupState(t, groupRequest(wc, "web-1", groupJSON(map[string]any{"action": "update", "id": id, "name": "Renamed", "defaultWorkDir": ""})))
	d := groupState(t, groupRequest(wc, "web-1", groupJSON(map[string]any{"action": "reorder", "orderedIds": []string{bid, id}})))
	if d.Groups[0].ID != bid {
		t.Fatal("reorder failed")
	}
	move["groupId"] = nil
	groupState(t, groupRequest(wc, "web-1", groupJSON(move)))
	e := groupState(t, groupRequest(wc, "web-1", groupJSON(map[string]any{"action": "delete", "id": bid})))
	if len(e.Memberships) != 0 || e.Revision != 7 {
		t.Fatalf("delete/revision: %+v", e)
	}
	other := groupState(t, groupRequest(wc, "web-2", "{}"))
	if other.Revision != 0 || len(other.Groups) != 0 {
		t.Fatal("cross-user read")
	}
	groupState(t, groupRequest(wc, "web-2", `{"action":"create","name":"Other"}`))
	persisted := groupState(t, groupRequest(&WebChannel{db: db}, "web-1", "{}"))
	if persisted.Revision != 7 || persisted.Groups[0].Name != "Renamed" {
		t.Fatal("persistence/isolation failed")
	}
}
func TestSessionGroupsInvalidAtomic(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	initial := groupState(t, groupRequest(wc, "web-1", `{"action":"convert","name":"A","sessions":[{"channel":"web","chatID":"owned"}]}`))
	id := initial.Groups[0].ID
	cases := []string{
		`{"action":"create","name":" "}`, `{"action":"create","name":"` + strings.Repeat("x", 257) + `"}`,
		`{"action":"create","name":"B","defaultWorkDir":"` + strings.Repeat("x", 4097) + `"}`,
		`{"action":"update","id":"missing","name":"B"}`, `{"action":"delete","id":"missing"}`,
		`{"action":"move","groupId":"missing","sessions":[{"channel":"web","chatID":"a"}]}`,
		`{"action":"move","sessions":[{"channel":"agent","chatID":"a"}]}`,
		`{"action":"move","sessions":[{"channel":"web","chatID":""}]}`,
		`{"action":"reorder","orderedIds":[]}`,
		groupJSON(map[string]any{"action": "reorder", "orderedIds": []string{id, id}}),
		`{"action":"convert","name":"B","sessions":[{"channel":"web","chatID":"new"},{"channel":"web","chatID":"owned"}]}`,
		`{"action":"bad"}`, `{"senderId":"web-2"}`, `{} {}`, `{`,
	}
	for _, body := range cases {
		w := groupRequest(wc, "web-1", body)
		if w.Code != 400 {
			t.Fatalf("expected 400 for %s: %d", body, w.Code)
		}
		s := groupState(t, groupRequest(wc, "web-1", ""))
		if groupJSON(s) != groupJSON(initial) {
			t.Fatal("invalid operation changed state")
		}
	}
	if w := groupRequest(wc, "", `{}`); w.Code != 401 {
		t.Fatal("missing auth accepted")
	}
	if w := groupRequest(wc, "web-1", `{"name":"`+strings.Repeat("x", maxBodySize)+`"}`); w.Code != 413 {
		t.Fatalf("oversize status %d", w.Code)
	}
}
func TestSessionGroupsConcurrent(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := groupRequest(wc, "web-1", fmt.Sprintf(`{"action":"convert","name":"G%d","sessions":[{"channel":"web","chatID":"c%d"}]}`, i, i))
			if w.Code != 200 {
				errs <- w.Body.String()
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	state := groupState(t, groupRequest(wc, "web-1", ""))
	if len(state.Groups) != n || len(state.Memberships) != n || state.Revision != n {
		t.Fatalf("lost updates: %+v", state)
	}
}
func TestSessionGroupsStorageFailure(t *testing.T) {
	db := newTestDB(t)
	wc := &WebChannel{db: db}
	initial := groupState(t, groupRequest(wc, "web-1", `{"action":"create","name":"A"}`))
	_, err := db.Exec(`CREATE TRIGGER reject_group_update BEFORE UPDATE ON user_settings BEGIN SELECT RAISE(FAIL,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if w := groupRequest(wc, "web-1", `{"action":"create","name":"B"}`); w.Code != 500 {
		t.Fatalf("write failure accepted: %s", w.Body.String())
	}
	if got := groupState(t, groupRequest(wc, "web-1", "")); groupJSON(got) != groupJSON(initial) {
		t.Fatal("write failure changed revision")
	}
	_, err = db.Exec(`DROP TRIGGER reject_group_update`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE user_settings SET value='not json' WHERE key=?`, sessionGroupsSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if w := groupRequest(wc, "web-1", `{"action":"create","name":"B"}`); w.Code != 500 {
		t.Fatal("corrupt data overwritten")
	}
}
func TestSessionGroupsForkInheritance(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	initial := groupState(t, groupRequest(wc, "web-1", `{"action":"convert","name":"A","sessions":[{"channel":"cli","chatID":"source"}]}`))
	calls := 0
	wc.callbacks.ChatFork = func(sender, channel, chatID, label string) (string, error) { calls++; return "destination", nil }
	fork := func(sender string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/chats/fork", strings.NewReader(`{"source_channel":"cli","source_chat_id":"source"}`))
		r = r.WithContext(contextWithSenderID(r.Context(), sender))
		w := httptest.NewRecorder()
		wc.handleChatsForkPOST(w, r)
		return w
	}
	if w := fork("web-1"); w.Code != 200 {
		t.Fatalf("fork: %s", w.Body.String())
	}
	state := groupState(t, groupRequest(wc, "web-1", ""))
	if state.Memberships["web:destination"] != initial.Groups[0].ID || state.Revision != 2 {
		t.Fatal("fork did not inherit")
	}
	if w := fork("web-2"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	other := groupState(t, groupRequest(wc, "web-2", ""))
	if len(other.Memberships) != 0 {
		t.Fatal("fork leaked other user's membership")
	}
	_, err := wc.db.Exec(`CREATE TRIGGER reject_group_update BEFORE UPDATE ON user_settings BEGIN SELECT RAISE(FAIL,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if w := fork("web-1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"chat_id":"destination"`) || !strings.Contains(w.Body.String(), "group_inheritance_error") {
		t.Fatalf("partial fork not explicit: %d %s", w.Code, w.Body.String())
	}
	wc.callbacks.ChatFork = func(string, string, string, string) (string, error) { return "", fmt.Errorf("copy failed") }
	if w := fork("web-1"); w.Code != 500 {
		t.Fatal("failed copy accepted")
	}
	if state = groupState(t, groupRequest(wc, "web-1", "")); state.Revision != 2 {
		t.Fatal("failed fork changed groups")
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}

func TestSessionGroupsAuthenticatedPOST(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	handler := wc.authenticatedPOST(wc.handleSessionGroups)
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(method, "/api/session-groups", strings.NewReader("{}")))
		want := http.StatusUnauthorized
		if method == "GET" {
			want = http.StatusMethodNotAllowed
		}
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", method, w.Code, want)
		}
	}
}

func TestSessionGroupsConcurrentMoves(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	state := groupState(t, groupRequest(wc, "web-1", `{"action":"create","name":"A"}`))
	id := state.Groups[0].ID
	const n = 24
	var wg sync.WaitGroup
	statuses := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := groupRequest(wc, "web-1", groupJSON(map[string]any{"action": "move", "groupId": id, "sessions": []sessionGroupSelector{{"web", fmt.Sprint(i)}}}))
			statuses <- w.Code
		}(i)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatal(status)
		}
	}
	state = groupState(t, groupRequest(wc, "web-1", ""))
	if len(state.Memberships) != n || state.Revision != n+1 {
		t.Fatalf("lost moves: %+v", state)
	}
}

func TestSessionGroupsCreatedGroupID(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	ids := map[string]bool{}
	for _, action := range []string{"create", "convert", "create"} {
		w := groupRequest(wc, "web-1", groupJSON(map[string]any{"action": action, "name": "same name"}))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var response sessionGroupsResponse
		decodeAPIData(t, w.Body, &response)
		id := response.CreatedGroupID
		if id == "" || ids[id] || id != response.Groups[len(response.Groups)-1].ID {
			t.Fatalf("ambiguous created id: %+v", response)
		}
		ids[id] = true
	}
	w := groupRequest(wc, "web-1", "{}")
	var response map[string]json.RawMessage
	decodeAPIData(t, w.Body, &response)
	if _, ok := response["createdGroupId"]; ok {
		t.Fatal("list returned a stale createdGroupId")
	}
	var raw string
	if err := wc.db.QueryRow(`SELECT value FROM user_settings WHERE channel='web' AND sender_id='web-1' AND key=?`, sessionGroupsSettingKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "createdGroupId") {
		t.Fatal("response-only createdGroupId persisted")
	}
}

func TestSessionGroupsRejectNULMetadata(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	for _, body := range []string{
		`{"action":"create","name":"a\u0000b"}`,
		`{"action":"create","name":"A","defaultWorkDir":"/a\u0000b"}`,
		`{"action":"convert","name":"A","sessions":[{"channel":"web","chatID":"a\u0000b"}]}`,
	} {
		if w := groupRequest(wc, "web-1", body); w.Code != 400 {
			t.Fatalf("NUL accepted: %s", w.Body.String())
		}
	}
	state := groupState(t, groupRequest(wc, "web-1", "{}"))
	if state.Revision != 0 || len(state.Groups) != 0 {
		t.Fatal("invalid NUL changed state")
	}
}

func TestSessionGroupsCorruptDocumentNeverOverwritten(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	cases := []struct{ name, raw string }{
		{"null", `null`},
		{"empty object", `{}`},
		{"missing revision", `{"groups":[],"memberships":{}}`},
		{"missing groups", `{"revision":7,"memberships":{}}`},
		{"missing memberships", `{"revision":7,"groups":[]}`},
		{"null revision", `{"revision":null,"groups":[],"memberships":{}}`},
		{"null groups", `{"revision":7,"groups":null,"memberships":{}}`},
		{"null memberships", `{"revision":7,"groups":[],"memberships":null}`},
		{"unknown field", `{"revision":7,"groups":[],"memberships":{},"unexpected":true}`},
		{"unknown nested field", `{"revision":7,"groups":[{"id":"a","name":"A","defaultWorkDir":"","unexpected":true}],"memberships":{}}`},
		{"invalid partial", `{"revision":7,"groups":`},
		{"wrong type", `{"revision":7,"groups":{},"memberships":{}}`},
		{"trailing value", `{"revision":7,"groups":[],"memberships":{}} {}`},
		{"duplicate ids", `{"revision":7,"groups":[{"id":"a"},{"id":"a"}],"memberships":{}}`},
		{"orphan membership", `{"revision":7,"groups":[],"memberships":{"web:c":"missing"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wc.db.Exec(`INSERT INTO user_settings(channel,sender_id,key,value,updated_at) VALUES('web','web-1',?,?,123) ON CONFLICT(channel,sender_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, sessionGroupsSettingKey, tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range []string{"", `{"action":"create","name":"B"}`, `{"action":"convert","name":"B","sessions":[{"channel":"web","chatID":"c"}]}`} {
				if w := groupRequest(wc, "web-1", body); w.Code != http.StatusInternalServerError {
					t.Fatalf("corrupt document accepted: %d %s", w.Code, w.Body.String())
				}
				var raw string
				var updated int64
				if err := wc.db.QueryRow(`SELECT value,updated_at FROM user_settings WHERE channel='web' AND sender_id='web-1' AND key=?`, sessionGroupsSettingKey).Scan(&raw, &updated); err != nil {
					t.Fatal(err)
				}
				if raw != tc.raw || updated != 123 {
					t.Fatalf("corrupt document was overwritten: %q timestamp %d", raw, updated)
				}
			}
		})
	}
}

func TestSessionGroupsUnknownRequestFieldsRejected(t *testing.T) {
	wc := &WebChannel{db: newTestDB(t)}
	for _, body := range []string{
		`{"action":"create","name":"A","unknown":true}`,
		`{"action":"convert","name":"A","sessions":[{"channel":"web","chatID":"c","unknown":true}]}`,
		`{"action":"create","name":`,
	} {
		if w := groupRequest(wc, "web-1", body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %d %s", w.Code, w.Body.String())
		}
	}
	state := groupState(t, groupRequest(wc, "web-1", "{}"))
	if state.Revision != 0 || len(state.Groups) != 0 || len(state.Memberships) != 0 {
		t.Fatal("invalid request persisted data")
	}
}
