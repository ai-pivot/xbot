# xbot-qq — QQ 渠道插件（QQ 官方开放平台 bot.q.qq.com）
#
# 零依赖 Go stdio 渠道插件。构建产出独立二进制；`make install` 把二进制与
# manifest 拷进 xbot 的用户插件目录（安装后需 reload plugins 才生效）。
#
#   make build    → bin/qq-plugin
#   make test     → 插件单测
#   make vet      → go vet
#   make install  → 拷到 ~/.xbot/plugins/xbot.qq/

BINARY    := bin/qq-plugin
PLUGIN_ID := xbot.qq
PLUGIN_DIR := $(HOME)/.xbot/plugins/$(PLUGIN_ID)

.PHONY: build test vet install clean

build:
	@mkdir -p bin
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

install: build
	@mkdir -p $(PLUGIN_DIR)/bin
	cp $(BINARY) $(PLUGIN_DIR)/bin/
	cp plugin.json $(PLUGIN_DIR)/
	@echo "Installed to $(PLUGIN_DIR). Reload plugins to activate: tui_control(action=reload_plugins)"

clean:
	rm -rf bin
