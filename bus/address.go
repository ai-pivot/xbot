package bus

import (
	"fmt"
	"strings"
)

// Address is a unified addressing identifier.
//
// Format: scheme://domain/id
//
// Examples:
//
//	im://feishu/ou_xxx       — Feishu user
//	im://feishu/oc_xxx       — Feishu group chat
//	im://qq/12345            — QQ user
//	agent://main             — Main Agent
//	agent://main/code-reviewer — SubAgent
//	system://cron            — Cron scheduler
type Address struct {
	Scheme string // "im", "agent", "system"
	Domain string // "feishu", "qq", "main", "cron"
	ID     string // Entity ID (may be empty, e.g. agent://main)
}

// Common address schemes.
const (
	SchemeIM     = "im"
	SchemeAgent  = "agent"
	SchemeSystem = "system"
)

// ParseAddress parses an address string.
//
//	"im://feishu/ou_xxx" → Address{Scheme:"im", Domain:"feishu", ID:"ou_xxx"}
//	"agent://main"       → Address{Scheme:"agent", Domain:"main", ID:""}
//	"agent://main/cr"    → Address{Scheme:"agent", Domain:"main", ID:"cr"}
func ParseAddress(s string) (Address, error) {
	// scheme://rest
	idx := strings.Index(s, "://")
	if idx < 0 {
		return Address{}, fmt.Errorf("invalid address %q: missing scheme", s)
	}
	scheme := s[:idx]
	rest := s[idx+3:] // domain[/id]

	// Validate scheme against known values
	switch scheme {
	case SchemeIM, SchemeAgent, SchemeSystem:
		// ok
	default:
		return Address{}, fmt.Errorf("invalid address %q: unknown scheme %q (valid: im, agent, system)", s, scheme)
	}

	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		// no id part: "agent://main"
		return Address{Scheme: scheme, Domain: rest}, nil
	}
	return Address{Scheme: scheme, Domain: rest[:slash], ID: rest[slash+1:]}, nil
}

// String returns the canonical address string.
func (a Address) String() string {
	if a.ID == "" {
		return a.Scheme + "://" + a.Domain
	}
	return a.Scheme + "://" + a.Domain + "/" + a.ID
}

// IsZero checks if the address is the zero value.
func (a Address) IsZero() bool {
	return a.Scheme == "" && a.Domain == "" && a.ID == ""
}

// IsIM checks if this is an IM channel address.
func (a Address) IsIM() bool {
	return a.Scheme == SchemeIM
}

// IsAgent checks if this is an Agent address.
func (a Address) IsAgent() bool {
	return a.Scheme == SchemeAgent
}

// IsSystem checks if this is a system address.
func (a Address) IsSystem() bool {
	return a.Scheme == SchemeSystem
}

// ChannelName returns the IM channel name (compatible with existing Channel field).
// For IM addresses, returns Domain (e.g. "feishu"); otherwise returns Scheme (e.g. "agent").
func (a Address) ChannelName() string {
	if a.Scheme == SchemeIM {
		return a.Domain
	}
	return a.Scheme
}

// --- Convenience constructors ---

// NewIMAddress creates an IM channel address.
//
//	NewIMAddress("feishu", "ou_xxx") → im://feishu/ou_xxx
func NewIMAddress(channel, id string) Address {
	return Address{Scheme: SchemeIM, Domain: channel, ID: id}
}

// NewAgentAddress creates an Agent address.
//
//	NewAgentAddress("main")    → agent://main
//	NewAgentAddress("main/cr") → agent://main/cr
func NewAgentAddress(path string) Address {
	slash := strings.IndexByte(path, '/')
	if slash < 0 {
		return Address{Scheme: SchemeAgent, Domain: path}
	}
	return Address{Scheme: SchemeAgent, Domain: path[:slash], ID: path[slash+1:]}
}

// NewSystemAddress creates a system address.
//
//	NewSystemAddress("cron") → system://cron
func NewSystemAddress(name string) Address {
	return Address{Scheme: SchemeSystem, Domain: name}
}

// --- Construct from existing fields (migration helpers) ---

// AddressFromChannelID constructs an Address from an existing (channel, id) pair.
// Used during migration to convert legacy Channel+ChatID/SenderID to unified addresses.
//
//	AddressFromChannelID("feishu", "oc_xxx") → im://feishu/oc_xxx
//	AddressFromChannelID("agent", "main/cr") → agent://main/cr
func AddressFromChannelID(channel, id string) Address {
	switch channel {
	case SchemeAgent:
		return NewAgentAddress(id)
	case SchemeSystem:
		return NewSystemAddress(id)
	default:
		return NewIMAddress(channel, id)
	}
}
