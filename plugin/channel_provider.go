package plugin

import (
	log "xbot/logger"
)

// ChannelProviderRegistrar 是将 ChannelProvider 注册到外部 registry 的回调函数。
// 由 serverapp 在初始化时注入，避免 plugin → channel 循环依赖。
// provider 参数类型为 any（实际为 channel.ChannelProvider），
// 由 serverapp 桥接层负责类型断言。
type ChannelProviderRegistrar func(provider any) error

// globalChannelProviderRegistrar 是全局 ChannelProvider 注册回调。
// 由 serverapp 在初始化时通过 SetChannelProviderRegistrar 设置。
var globalChannelProviderRegistrar ChannelProviderRegistrar

// SetChannelProviderRegistrar 设置全局 ChannelProvider 注册回调。
// 在 serverapp.Run() 中调用，将 plugin 包连接到 ChannelProviderRegistry。
func SetChannelProviderRegistrar(fn ChannelProviderRegistrar) {
	globalChannelProviderRegistrar = fn
}

// WireChannelProviders 将所有已激活插件的 ChannelProvider
// 连接到外部 registry（通过 SetChannelProviderRegistrar 注入的回调）。
// 在 PluginManager.ActivateAll() 之后调用。
func WireChannelProviders(pm *PluginManager) {
	if globalChannelProviderRegistrar == nil {
		log.Debug("ChannelProviderRegistrar not set, skipping channel provider wiring")
		return
	}

	pm.mu.RLock()
	entries := make([]*PluginEntry, 0, len(pm.entries))
	for _, e := range pm.entries {
		if e.State == StateActive {
			entries = append(entries, e)
		}
	}
	pm.mu.RUnlock()

	for _, entry := range entries {
		ctx := entry.Context
		if ctx == nil {
			continue
		}
		providers := ctx.ChannelProviders()
		for _, p := range providers {
			type nameable interface{ Name() string }
			n, _ := p.(nameable)
			channelName := "<unknown>"
			if n != nil {
				channelName = n.Name()
			}
			if err := globalChannelProviderRegistrar(p); err != nil {
				log.WithField("plugin", entry.Manifest.ID).
					WithField("channel", channelName).
					WithError(err).Warn("Failed to register channel provider")
				continue
			}
			log.WithField("plugin", entry.Manifest.ID).
				WithField("channel", channelName).
				Info("Channel provider registered via plugin")
		}
	}
}

// CollectChannelProviders 收集所有插件的 ChannelProvider（不注册到 registry）。
// 用于测试或需要延迟注册的场景。
func CollectChannelProviders(pm *PluginManager) []any {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var result []any
	for _, entry := range pm.entries {
		if entry.State != StateActive {
			continue
		}
		ctx := entry.Context
		if ctx == nil {
			continue
		}
		result = append(result, ctx.ChannelProviders()...)
	}
	return result
}
