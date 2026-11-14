package main

// 阶段 3 实跑：本地起**真实 net.Listen** 的假官方 WebSocket 网关（零外部依赖），
// 用真实 stdlibDialer 跑完整生命周期：
//
//	Hello(op10) → Identify(op2) → READY → 业务事件 → 主动断开
//	→ 1s/2s/4s 退避 → 重连 → **op6 Resume**（session_id/seq 与断线前一致）
//	→ 服务端补发断线期间遗漏的事件 → RESUMED
//	→ op9 Invalid Session → 重取 token → 重连 → Identify
//	→ 连续失败 N 次后取消 → Run 退出、goroutine 回基线
//
// 日志落到 court/evidence/gongbu/qq-channel-plugin/reconnect-live.log（绝对路径），
// 便于验收时直接查看。

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 极简 WebSocket 服务端（只做本用例需要的部分）
// ---------------------------------------------------------------------------

// wsServerConn 是服务端侧的一条连接。
type wsServerConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// upgrade 完成 HTTP → WebSocket 握手（Sec-WebSocket-Accept 用 wsclient.go 的 wsAccept）。
func wsUpgrade(conn net.Conn) (*wsServerConn, error) {
	br := bufio.NewReader(conn)
	var key string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Sec-WebSocket-Key") {
			key = strings.TrimSpace(v)
		}
	}
	if key == "" {
		return nil, fmt.Errorf("no Sec-WebSocket-Key in handshake")
	}
	accept := wsAcceptServer(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := io.WriteString(conn, resp); err != nil {
		return nil, err
	}
	return &wsServerConn{conn: conn, br: br}, nil
}

// wsAcceptServer 与客户端 wsAccept 同算法（复用 wsAcceptGUID）。
func wsAcceptServer(key string) string {
	h := sha1.New()
	io.WriteString(h, key+wsAcceptGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// readFrame 读一帧（客户端帧必须带 mask，这里负责解掩码）。
func (c *wsServerConn) readFrame() (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	n := int(hdr[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// writeText 发一条文本帧（服务端帧不掩码）。
func (c *wsServerConn) writeText(s string) error {
	payload := []byte(s)
	var frame []byte
	frame = append(frame, 0x81) // FIN + text
	switch {
	case len(payload) < 126:
		frame = append(frame, byte(len(payload)))
	case len(payload) < 1<<16:
		frame = append(frame, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(payload)))
		frame = append(frame, 127)
		frame = append(frame, ext[:]...)
	}
	_, err := c.conn.Write(append(frame, payload...))
	return err
}

// readJSON 读一帧并解析成 map（跳过 ping/pong）。
func (c *wsServerConn) readJSON() (map[string]any, error) {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case wsOpPing, wsOpPong:
			continue
		case wsOpClose:
			return nil, io.EOF
		}
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil, fmt.Errorf("decode client frame: %w (%s)", err, payload)
		}
		return m, nil
	}
}

// liveLog 收集实跑日志并写盘。
type liveLog struct {
	mu  sync.Mutex
	buf strings.Builder
	t   *testing.T
}

func (l *liveLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf("%s %s", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), fmt.Sprintf(format, args...))
	l.buf.WriteString(line + "\n")
	if l.t != nil {
		l.t.Log(line)
	}
}

func (l *liveLog) flush(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(l.buf.String()), 0o644)
}

// ---------------------------------------------------------------------------
// 实跑
// ---------------------------------------------------------------------------

// TestReconnectLive 真实 TCP + WebSocket 握手下的完整重连剧本。
func TestReconnectLive(t *testing.T) {
	log := &liveLog{t: t}
	evidence := "/home/smith/.xbot/court/evidence/gongbu/qq-channel-plugin/reconnect-live.log"
	defer func() {
		if err := log.flush(evidence); err != nil {
			t.Logf("flush evidence log: %v", err)
		} else {
			t.Logf("evidence log written: %s", evidence)
		}
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	log.add("fake official gateway listening on %s (real net.Listen)", addr)

	scenarios := make([]func(*wsServerConn, *liveLog), 3)
	scenarios[0] = func(c *wsServerConn, lg *liveLog) {
		lg.add("conn#1: send op10 Hello (heartbeat_interval=2000)")
		_ = c.writeText(`{"op":10,"d":{"heartbeat_interval":2000}}`)

		lg.add("conn#1: awaiting auth frame")
		f, err := c.readJSON()
		if err != nil {
			lg.add("conn#1: read auth error: %v", err)
			return
		}
		op := int(f["op"].(float64))
		d, _ := f["d"].(map[string]any)
		lg.add("conn#1: got op=%d (identify=2) token=%v intents=%v shard=%v", op, d["token"], d["intents"], d["shard"])
		if op != opIdentify {
			lg.add("conn#1: UNEXPECTED: first connect must be identify")
			return
		}

		lg.add("conn#1: send op0 t=READY session_id=sess-live-1")
		_ = c.writeText(`{"op":0,"s":1,"t":"READY","d":{"session_id":"sess-live-1","user":{"id":"bot"}}}`)
		lg.add("conn#1: push event ev-10 (s=10) and ev-11 (s=11)")
		_ = c.writeText(`{"op":0,"s":10,"t":"C2C_MESSAGE_CREATE","id":"ev-10","d":{"id":"m-10","content":"before-disconnect-1","author":{"user_openid":"U1"}}}`)
		_ = c.writeText(`{"op":0,"s":11,"t":"C2C_MESSAGE_CREATE","id":"ev-11","d":{"id":"m-11","content":"before-disconnect-2","author":{"user_openid":"U1"}}}`)
		time.Sleep(200 * time.Millisecond)
		lg.add("conn#1: abruptly closing connection (simulating network drop)")
		_ = c.conn.Close()
	}

	scenarios[1] = func(c *wsServerConn, lg *liveLog) {
		lg.add("conn#2: send op10 Hello")
		_ = c.writeText(`{"op":10,"d":{"heartbeat_interval":2000}}`)

		lg.add("conn#2: awaiting auth frame (expect op6 resume)")
		f, err := c.readJSON()
		if err != nil {
			lg.add("conn#2: read auth error: %v", err)
			return
		}
		op := int(f["op"].(float64))
		d, _ := f["d"].(map[string]any)
		lg.add("conn#2: got op=%d (resume=6) session_id=%v seq=%v token=%v", op, d["session_id"], d["seq"], d["token"])
		if op != opResume {
			lg.add("conn#2: UNEXPECTED: reconnect must use op6 resume, not identify")
			return
		}

		lg.add("conn#2: replay missed events s=12 (m-12) and s=13 (m-13)")
		_ = c.writeText(`{"op":0,"s":12,"t":"C2C_MESSAGE_CREATE","id":"ev-12","d":{"id":"m-12","content":"missed-during-drop-1","author":{"user_openid":"U1"}}}`)
		_ = c.writeText(`{"op":0,"s":13,"t":"C2C_MESSAGE_CREATE","id":"ev-13","d":{"id":"m-13","content":"missed-during-drop-2","author":{"user_openid":"U1"}}}`)
		lg.add("conn#2: send RESUMED (backfill complete)")
		_ = c.writeText(`{"op":0,"s":13,"t":"RESUMED","d":{}}`)
		time.Sleep(300 * time.Millisecond)

		lg.add("conn#2: send op9 invalid session (forces token refetch + identify)")
		_ = c.writeText(`{"op":9,"d":false}`)
		time.Sleep(200 * time.Millisecond)
		_ = c.conn.Close()
	}

	scenarios[2] = func(c *wsServerConn, lg *liveLog) {
		lg.add("conn#3: send op10 Hello")
		_ = c.writeText(`{"op":10,"d":{"heartbeat_interval":2000}}`)
		f, err := c.readJSON()
		if err != nil {
			lg.add("conn#3: read auth error: %v", err)
			return
		}
		op := int(f["op"].(float64))
		d, _ := f["d"].(map[string]any)
		lg.add("conn#3: got op=%d (identify=2 after op9) token=%v", op, d["token"])
		lg.add("conn#3: closing (test done)")
		_ = c.conn.Close()
	}

	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
		defer ln.Close() // 剧本跑完就关监听，避免 Accept 永久阻塞
		for i := 0; i < len(scenarios); i++ {
			conn, err := ln.Accept()
			if err != nil {
				log.add("accept #%d error: %v", i+1, err)
				return
			}
			sc, err := wsUpgrade(conn)
			if err != nil {
				log.add("handshake #%d error: %v", i+1, err)
				_ = conn.Close()
				continue
			}
			scenarios[i](sc, log)
		}
	}()

	// ── 客户端：真实 dialer ──
	baseline := runtime.NumGoroutine()
	log.add("goroutines before start: %d", baseline)

	tokens := &recordingTokens{}
	events := make(chan GatewayEvent, 16)
	gw := NewGateway(tokens, func(context.Context) (string, error) {
		return "ws://" + addr + "/websocket", nil
	}, stdlibDialer{HandshakeTimeout: 3 * time.Second}, func(_ context.Context, ev GatewayEvent) {
		events <- ev
	}, func(format string, args ...any) { log.add("[gateway] "+format, args...) })
	var backoffMu sync.Mutex
	var backoffs []time.Duration
	gw.cfg = GatewayConfig{
		Reconnect: ReconnectPolicy{Min: 1 * time.Second, Max: 4 * time.Second},
		// 记录真实退避序列，但把等待压到 50ms（剧本本身要快；退避算法照旧计算）。
		Sleep: func(ctx context.Context, d time.Duration) bool {
			backoffMu.Lock()
			backoffs = append(backoffs, d)
			backoffMu.Unlock()
			log.add("backoff sleep requested: %s", d)
			select {
			case <-ctx.Done():
				return false
			case <-time.After(50 * time.Millisecond):
				return true
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = gw.Run(ctx) }()

	// 收事件：连接#1 的 2 条 + 连接#2 补发的 2 条 = 4 条。
	seen := map[string]int64{}
	deadline := time.After(12 * time.Second)
	for len(seen) < 4 {
		select {
		case ev := <-events:
			seen[ev.ID] = ev.Sequence
			log.add("client received event id=%s type=%s seq=%d", ev.ID, ev.Type, ev.Sequence)
		case <-deadline:
			t.Fatalf("timeout waiting events, got %v", seen)
		}
	}
	for _, id := range []string{"ev-10", "ev-11", "ev-12", "ev-13"} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing event %s (resume backfill lost): %v", id, seen)
		}
	}
	if seen["ev-12"] != 12 || seen["ev-13"] != 13 {
		t.Fatalf("backfilled seq wrong: %v", seen)
	}
	log.add("all 4 events received (2 pre-drop + 2 backfilled after resume)")

	// 等服务端把 3 个剧本（含 op9 → identify）跑完。
	select {
	case <-srvDone:
		log.add("fake gateway sent all 3 scenarios and closed listener")
	case <-time.After(10 * time.Second):
		log.add("fake gateway did not finish in time; closing listener")
		_ = ln.Close()
	}

	// ── 连续失败阶段：监听已关闭，客户端会持续退避重试 → 1s/2s/4s ──
	deadline2 := time.After(8 * time.Second)
	var tail []time.Duration
	for len(tail) < 4 {
		backoffMu.Lock()
		tail = append([]time.Duration(nil), backoffs...)
		backoffMu.Unlock()
		if len(tail) >= 4 {
			break
		}
		select {
		case <-deadline2:
			log.add("WARN: only %d backoff samples in 8s", len(tail))
			goto stopped
		case <-time.After(20 * time.Millisecond):
		}
	}
	// 取最后三次（第一次重连成功前的 1s 不计入"连续失败"序列）。
	tail = tail[len(tail)-3:]
	log.add("consecutive-failure backoff sequence: %s / %s / %s", tail[0], tail[1], tail[2])
	if tail[0] != time.Second || tail[1] != 2*time.Second || tail[2] != 4*time.Second {
		t.Fatalf("backoff sequence = %v, want 1s/2s/4s (指数退避)", tail)
	}

stopped:
	cancel()
	select {
	case <-done:
		log.add("gateway Run exited after cancel")
	case <-time.After(5 * time.Second):
		t.Fatal("gateway Run did not exit")
	}

	// goroutine 回基线（允许少量运行时抖动）。
	for i := 0; i < 20; i++ {
		if runtime.NumGoroutine() <= baseline+2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	after := runtime.NumGoroutine()
	log.add("goroutines after stop: %d (baseline %d)", after, baseline)
	if after > baseline+2 {
		t.Fatalf("goroutine leak: before=%d after=%d", baseline, after)
	}
	log.add("RESULT: PASS")
}
