package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWSAcceptRFCVector 用 RFC 6455 的标准示例向量校验握手摘要。
func TestWSAcceptRFCVector(t *testing.T) {
	got := wsAccept("dGhlIHNhbXBsZSBub25jZQ==")
	if got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("wsAccept = %q", got)
	}
}

// wsTestServer 起一个手写升级的 WS 服务端，把连接交给 handler。
func wsTestServer(t *testing.T, handler func(r *http.Request, conn net.Conn, br *bufio.Reader)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("Upgrade = %q", r.Header.Get("Upgrade"))
			http.Error(w, "not a websocket", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer is not hijackable")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", wsAccept(r.Header.Get("Sec-WebSocket-Key")))
		_ = brw.Flush()
		handler(r, conn, brw.Reader)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWSDialAndTextFrame 端到端验握手 + 客户端掩码帧 + 服务端文本帧。
func TestWSDialAndTextFrame(t *testing.T) {
	received := make(chan string, 1)
	srv := wsTestServer(t, func(_ *http.Request, conn net.Conn, br *bufio.Reader) {
		op, payload, masked, err := readClientFrame(br)
		if err != nil {
			t.Errorf("read client frame: %v", err)
			return
		}
		if op != wsOpText || !masked {
			t.Errorf("op=%d masked=%t, want text+masked", op, masked)
			return
		}
		received <- string(payload)
		_ = writeServerFrame(conn, wsOpText, []byte("echo:"+string(payload)))
	})

	d := stdlibDialer{}
	conn, err := d.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "echo:hello" {
		t.Fatalf("message = %q", msg)
	}
	if got := <-received; got != "hello" {
		t.Fatalf("server received %q", got)
	}
}

// TestWSHandlesPingAndFragmentation 客户端必须回 pong，且拼装分片消息。
func TestWSHandlesPingAndFragmentation(t *testing.T) {
	srv := wsTestServer(t, func(_ *http.Request, conn net.Conn, br *bufio.Reader) {
		_ = writeServerFrameEx(conn, wsOpPing, []byte("p"), true)
		_ = writeServerFrameEx(conn, wsOpText, []byte("he"), false)
		_ = writeServerFrameEx(conn, wsOpContinuation, []byte("llo"), true)

		// 客户端应当先回 pong（payload 原样），再继续读分片。
		op, payload, _, err := readClientFrame(br)
		if err != nil {
			t.Errorf("read pong: %v", err)
			return
		}
		if op != wsOpPong || string(payload) != "p" {
			t.Errorf("pong op=%d payload=%q", op, payload)
		}
	})

	d := stdlibDialer{}
	conn, err := d.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "hello" {
		t.Fatalf("fragmented message = %q, want hello", msg)
	}
}

// TestWSDialRejectsNon101 非 101 必须报错，不能当成已连接。
func TestWSDialRejectsNon101(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	d := stdlibDialer{HandshakeTimeout: 5 * time.Second}
	_, err := d.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want HTTP 403", err)
	}
}

// TestWSRejectsBadAccept 服务端 accept 头不对（或缺失）必须报错 —— 防中间人。
func TestWSRejectsBadAccept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj := w.(http.Hijacker)
		conn, brw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"+
			"Connection: Upgrade\r\nSec-WebSocket-Accept: bogus\r\n\r\n")
		_ = brw.Flush()
	}))
	defer srv.Close()

	d := stdlibDialer{HandshakeTimeout: 5 * time.Second}
	if _, err := d.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil); err == nil {
		t.Fatal("want error for bad Sec-WebSocket-Accept")
	}
}

// ---------------------------------------------------------------------------
// 测试用帧编解码（服务端视角：写不掩码帧、读掩码帧）
// ---------------------------------------------------------------------------

func writeServerFrame(w io.Writer, opcode byte, payload []byte) error {
	return writeServerFrameEx(w, opcode, payload, true)
}

func writeServerFrameEx(w io.Writer, opcode byte, payload []byte, fin bool) error {
	hdr := []byte{opcode}
	if fin {
		hdr[0] |= 0x80
	}
	switch n := len(payload); {
	case n <= 125:
		hdr = append(hdr, byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(hdr, ext[:]...)
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readClientFrame(br *bufio.Reader) (opcode byte, payload []byte, masked bool, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return 0, nil, false, err
	}
	opcode = hdr[0] & 0x0F
	masked = hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, false, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, false, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(br, mask[:]); err != nil {
			return 0, nil, false, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(br, payload); err != nil {
		return 0, nil, false, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, masked, nil
}
