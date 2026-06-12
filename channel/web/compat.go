package web

import (
	"xbot/channel"
)

// Re-export shared types from channel package.
type (
	OutboundMsg        = channel.OutboundMsg
	InboundMsg         = channel.InboundMsg
	BgTask             = channel.BgTask
	BgTaskStatus       = channel.BgTaskStatus
	SessionChatMessage = channel.SessionChatMessage
	SessionStateSender = channel.SessionStateSender
	SettingDefinition  = channel.SettingDefinition
)

var (
	ConvertFeishuCard = channel.ConvertFeishuCard
	AllSettingDefs    = channel.AllSettingDefs
)
