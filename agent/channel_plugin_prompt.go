package agent

import (
	"context"
	"sync"
)

// channelPluginPromptProvider 实现 ChannelPromptProvider 接口，
// 供 channel 插件动态声明系统 prompt 片段。
// 线程安全：通过 sync.RWMutex 保护 systemParts 的读写。
type channelPluginPromptProvider struct {
	mu          sync.RWMutex
	channelName string
	systemParts map[string]string
}

// newChannelPluginPromptProvider 创建一个新的 channel 插件 prompt 提供者。
// 初始时 systemParts 为空，插件通过 SetSystemParts 动态更新。
func newChannelPluginPromptProvider(channelName string) *channelPluginPromptProvider {
	return &channelPluginPromptProvider{
		channelName: channelName,
	}
}

// ChannelPromptName 返回 channel 名称。
func (p *channelPluginPromptProvider) ChannelPromptName() string {
	return p.channelName
}

// ChannelSystemParts 返回 channel 特化的 system prompt 片段。
// 如果插件尚未发送 channel_prompt 声明，返回 nil。
func (p *channelPluginPromptProvider) ChannelSystemParts(_ context.Context, _, _ string) map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.systemParts) == 0 {
		return nil
	}
	// 返回浅拷贝，避免调用方修改内部状态
	result := make(map[string]string, len(p.systemParts))
	for k, v := range p.systemParts {
		result[k] = v
	}
	return result
}

// setSystemParts 设置 system prompt 片段（线程安全）。
// channel 插件发送新的 channel_prompt 时会覆盖整个片段集合（hot-update）。
func (p *channelPluginPromptProvider) setSystemParts(parts map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.systemParts = make(map[string]string, len(parts))
	for k, v := range parts {
		p.systemParts[k] = v
	}
}
