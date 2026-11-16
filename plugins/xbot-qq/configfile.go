package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// mergeConfigFile 用 config.json 里 channels.qq 的值补齐 raw 中的**空/缺失**键。
//
// 背景：host 的 Config 结构体在进程启动时从 config.json 加载一次；之后若有人
// 直接改文件（而不是走 set_channel_config RPC），host 内存里就没有新键，
// 推给插件的 channel_config 里也就没有 —— 表现为"改了配置但不生效"。
// 插件自己读一遍文件作为兜底，既不依赖主模块，也让"改文件 + reload"可用。
//
// 优先级：host 推来的**非空**值 > 文件里的值。这样面板实时修改仍然立即生效。
func mergeConfigFile(raw map[string]string) {
	if raw == nil {
		return
	}
	home := strings.TrimSpace(os.Getenv("XBOT_HOME"))
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".xbot")
		}
	}
	if home == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		return
	}
	var doc struct {
		Channels map[string]map[string]string `json:"channels"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return
	}
	for k, v := range doc.Channels["qq"] {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if cur, ok := raw[k]; !ok || strings.TrimSpace(cur) == "" {
			raw[k] = v
		}
	}
}
