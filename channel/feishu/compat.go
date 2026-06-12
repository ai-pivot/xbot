package feishu

import (
	"xbot/channel"
)

// Re-export shared types from channel package.
type (
	OutboundMsg         = channel.OutboundMsg
	InboundMsg          = channel.InboundMsg
	BgTask              = channel.BgTask
	BgTaskStatus        = channel.BgTaskStatus
	Subscription        = channel.Subscription
	PerModelConfig      = channel.PerModelConfig
	SubscriptionManager = channel.SubscriptionManager
	LLMSubscriber       = channel.LLMSubscriber
	SettingDefinition   = channel.SettingDefinition
	SettingType         = channel.SettingType
	AskQItem            = channel.AskQItem
)

const (
	SettingTypeToggle   = channel.SettingTypeToggle
	SettingTypeSelect   = channel.SettingTypeSelect
	SettingTypeCombo    = channel.SettingTypeCombo
	SettingTypeText     = channel.SettingTypeText
	SettingTypePassword = channel.SettingTypePassword
)

var (
	ConvertFeishuCard = channel.ConvertFeishuCard
)
