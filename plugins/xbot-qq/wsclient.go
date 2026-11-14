package main

// 最小 WebSocket 客户端（RFC 6455 子集，只用 stdlib）。
//
// 说明：QQ 官方文档只规定了网关地址与 opcode（见 gateway.go 头部引用），
// WebSocket 握手/帧格式本身由 RFC 6455 规定，不在 QQ 文档范围内。
// 本实现覆盖：握手（Sec-WebSocket-Accept 校验）、客户端帧掩码、分片消息、
// ping/pong、close。
//
// 为什么不用 gorilla/websocket：本插件一直是**零依赖**模块（go.mod 只有
// `module xbot-qq` + `go 1.22`），安装到插件目录时不希望带 vendor/网络拉包。
// 官方 QQ 网关只用文本帧 + ping/pong/close，不需要扩展协商、不需要压缩，
// 自己实现 200 行足够，而且握手与帧格式都能用标准库的 httptest 验证。
//
// 支持：
//   - ws / wss（含默认端口、query、自定义头）
//   - 客户端帧掩码（协议强制）
//   - 分片消息（FIN=0 + continuation）
//   - ping → 自动回 pong；close → 回 close 并返回 io.EOF
//
// 不支持（当前用不到）：扩展协商（permessage-deflate）、二进制业务消息、
// 服务端掩码帧（协议禁止，收到即报错）。

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA

	wsAcceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

// WSConn 是 Gateway 依赖的连接抽象（便于单测替换成内存实现）。
type WSConn interface {
	// ReadMessage 返回一条完整文本消息；控制帧在内部处理。
	ReadMessage() ([]byte, error)
	// WriteMessage 发送一条文本消息。
	WriteMessage(payload []byte) error
	// SetReadDeadline 设置下一次读的超时（心跳失联检测用）。
	SetReadDeadline(t time.Time) error
	Close() error
}

// Dialer 建立 WebSocket 连接。
type Dialer interface {
	Dial(ctx context.Context, rawURL string, header http.Header) (WSConn, error)
}

// stdlibDialer 是 Dialer 的生产实现。
type stdlibDialer struct {
	// HandshakeTimeout 限制 TCP/TLS + 握手阶段；0 用默认 15s。
	HandshakeTimeout time.Duration
}

func (d stdlibDialer) Dial(ctx context.Context, rawURL string, header http.Header) (WSConn, error) {
	timeout := d.HandshakeTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse ws url: %w", err)
	}
	var secure bool
	switch strings.ToLower(u.Scheme) {
	case "wss":
		secure = true
	case "ws":
		secure = false
	default:
		return nil, fmt.Errorf("unsupported ws scheme %q (want ws/wss)", u.Scheme)
	}

	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		if secure {
			host = net.JoinHostPort(host, "443")
		} else {
			host = net.JoinHostPort(host, "80")
		}
	}

	dialer := &net.Dialer{}
	rawConn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}
	conn := rawConn
	if secure {
		tlsConn := tls.Client(rawConn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("tls handshake %s: %w", u.Hostname(), err)
		}
		conn = tlsConn
	}

	key, err := wsKey()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	var req strings.Builder
	req.WriteString("GET " + path + " HTTP/1.1\r\n")
	req.WriteString("Host: " + u.Host + "\r\n")
	req.WriteString("Upgrade: websocket\r\n")
	req.WriteString("Connection: Upgrade\r\n")
	req.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	req.WriteString("Sec-WebSocket-Version: 13\r\n")
	for k, vs := range header {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Upgrade") ||
			strings.EqualFold(k, "Connection") || strings.HasPrefix(strings.ToLower(k), "sec-websocket-") {
			continue // 这些头由本函数自己写，避免重复/覆盖
		}
		for _, v := range vs {
			req.WriteString(k + ": " + v + "\r\n")
		}
	}
	req.WriteString("\r\n")

	if _, err := io.WriteString(conn, req.String()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write handshake: %w", err)
	}

	br := bufio.NewReader(conn)
	httpReq := &http.Request{Method: http.MethodGet, URL: u, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}}
	resp, err := http.ReadResponse(br, httpReq)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read handshake response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("ws handshake: HTTP %d", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), wsAccept(key); got != want {
		_ = conn.Close()
		return nil, fmt.Errorf("ws handshake: bad Sec-WebSocket-Accept %q (want %q)", got, want)
	}

	return &wsConn{conn: conn, br: br}, nil
}

// wsKey 生成 16 字节随机 Sec-WebSocket-Key。
func wsKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate ws key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// wsAccept 计算服务端应回的 Sec-WebSocket-Accept。
func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsAcceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// wsConn 是 WSConn 的 stdlib 实现。
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	// writeMu 序列化写帧：心跳协程写 op=1，读循环收到 ping 时写 op=10，
	// 两条路并发写同一连接会交错出坏帧。
	writeMu sync.Mutex
}

func (c *wsConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

func (c *wsConn) WriteMessage(payload []byte) error {
	return c.writeFrame(wsOpText, payload)
}

// Close 尽力发一个 close 帧，然后关连接。
func (c *wsConn) Close() error {
	_ = c.writeFrame(wsOpClose, []byte{0x03, 0xE8}) // 1000 normal closure
	return c.conn.Close()
}

// ReadMessage 读一条完整文本消息。控制帧（ping/pong/close）在内部处理。
func (c *wsConn) ReadMessage() ([]byte, error) {
	var msg []byte
	started := false
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case wsOpPing:
			// 协议要求回同 payload 的 pong（掩码由 writeFrame 处理）。
			if err := c.writeFrame(wsOpPong, payload); err != nil {
				return nil, err
			}
			continue
		case wsOpPong:
			continue
		case wsOpClose:
			_ = c.writeFrame(wsOpClose, payload)
			return nil, io.EOF
		case wsOpText, wsOpBinary:
			if started {
				return nil, errors.New("ws: new data frame before previous message finished")
			}
			started = true
			msg = append(msg, payload...)
		case wsOpContinuation:
			if !started {
				return nil, errors.New("ws: continuation frame without initial frame")
			}
			msg = append(msg, payload...)
		default:
			return nil, fmt.Errorf("ws: unsupported opcode %d", op)
		}
		if fin {
			return msg, nil
		}
	}
}

// readFrame 读一帧（已解掩码）。
func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if length < 0 || length > 32<<20 {
		return false, 0, nil, fmt.Errorf("ws: frame too large (%d bytes)", length)
	}
	if opcode >= 0x8 && length > 125 {
		return false, 0, nil, fmt.Errorf("ws: control frame too long (%d bytes)", length)
	}

	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// writeFrame 写一帧（客户端必须掩码）。
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var hdr []byte
	hdr = append(hdr, 0x80|opcode) // FIN=1

	n := len(payload)
	switch {
	case n <= 125:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(hdr, ext[:]...)
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("ws: generate mask: %w", err)
	}
	hdr = append(hdr, mask[:]...)

	frame := make([]byte, 0, len(hdr)+n)
	frame = append(frame, hdr...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, err := c.conn.Write(frame)
	return err
}
