# xbot-genui — GenUI (display_html) channel plugin
#
# Zero-dependency Go stdio channel plugin. Build produces a standalone binary;
# `make install` copies it into the xbot user plugin directory.
#
#   make build    → bin/genui-plugin
#   make test     → run plugin unit tests
#   make install  → copy binary to ~/.xbot/plugins/xbot.genui/bin/

BINARY   := bin/genui-plugin
PLUGIN_ID := xbot.genui
PLUGIN_DIR := $(HOME)/.xbot/plugins/$(PLUGIN_ID)

.PHONY: build test install clean

build:
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/genui-plugin

test:
	go test ./...

install: build
	@mkdir -p $(PLUGIN_DIR)/bin
	cp $(BINARY) $(PLUGIN_DIR)/bin/
	cp plugin.json $(PLUGIN_DIR)/
	@echo "Installed to $(PLUGIN_DIR). Reload plugins to activate: tui_control(action=reload_plugins)"

clean:
	rm -rf bin
