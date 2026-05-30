package serverapp

import (
	"fmt"
	"sync"

	"xbot/channel"
)

// ChannelProviderRegistry 管理 ChannelProvider 的注册与查找。
// 内置 channel（feishu/qq/napcat/web）不经过此 registry，
// 只有插件注册的 ChannelProvider 才存储在这里。
// 全局单例，通过 SetChannelProviderRegistry / GetChannelProviderRegistry 访问。
type ChannelProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]channel.ChannelProvider
}

// globalChannelProviderRegistry 是全局 ChannelProvider 注册表。
// 由 serverapp 在初始化时设置，plugin 包在注册时读取。
var globalChannelProviderRegistry *ChannelProviderRegistry

// SetChannelProviderRegistry 设置全局 ChannelProvider 注册表。
// 在 serverapp.InitServer 中调用一次。
func SetChannelProviderRegistry(reg *ChannelProviderRegistry) {
	globalChannelProviderRegistry = reg
}

// GetChannelProviderRegistry 返回全局 ChannelProvider 注册表。
// 返回 nil 表示未初始化（非 server 模式）。
func GetChannelProviderRegistry() *ChannelProviderRegistry {
	return globalChannelProviderRegistry
}

// NewChannelProviderRegistry 创建空的 ChannelProvider 注册表。
func NewChannelProviderRegistry() *ChannelProviderRegistry {
	return &ChannelProviderRegistry{
		providers: make(map[string]channel.ChannelProvider),
	}
}

// Register 注册一个 ChannelProvider。
// 如果同名 provider 已存在，返回错误。
func (r *ChannelProviderRegistry) Register(provider channel.ChannelProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := provider.Name()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("channel provider %q already registered", name)
	}
	r.providers[name] = provider
	return nil
}

// Get 根据 name 查找 ChannelProvider。
func (r *ChannelProviderRegistry) Get(name string) (channel.ChannelProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// List 返回所有已注册的 ChannelProvider。
func (r *ChannelProviderRegistry) List() []channel.ChannelProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]channel.ChannelProvider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}
