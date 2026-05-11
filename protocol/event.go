package protocol

import "encoding/json"

// TransportEvent 是所有跨层事件的统一接口。
type TransportEvent interface {
	EventType() string
	EventVersion() int
}

// EventEnvelope 是事件容器。local/remote 统一走 JSON 序列化。
type EventEnvelope struct {
	Type    string          `json:"type"`
	Version int             `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

// EventHandler 原始回调签名。
type EventHandler func(event EventEnvelope)

// EventPattern 订阅匹配规则。
type EventPattern struct {
	Type       string
	MinVersion int
	MaxVersion int
}

func (p EventPattern) Matches(typ string, ver int) bool {
	if p.Type != "" && p.Type != typ {
		return false
	}
	if p.MinVersion > 0 && ver < p.MinVersion {
		return false
	}
	if p.MaxVersion > 0 && ver > p.MaxVersion {
		return false
	}
	return true
}
