# xbot-git-fancy — Git Fancy stdio plugin
#
# Zero-dependency Go stdio plugin. `make install` copies the binary + plugin.json
# into the xbot user plugin directory.
#
#   make build    → bin/git-fancy-plugin
#   make test     → run unit tests (if any)
#   make install  → copy binary to ~/.xbot/plugins/xbot.git-fancy/bin/

BINARY   := bin/git-fancy-plugin
PLUGIN_ID := xbot.git-fancy
PLUGIN_DIR := $(HOME)/.xbot/plugins/$(PLUGIN_ID)

.PHONY: build test install clean

build:
	@mkdir -p bin
	go build -o $(BINARY) .

test:
	go test ./...

install: build
	@mkdir -p $(PLUGIN_DIR)/bin
	cp $(BINARY) $(PLUGIN_DIR)/bin/
	cp plugin.json $(PLUGIN_DIR)/
	@echo "Installed to $(PLUGIN_DIR). Reload plugins to activate."

clean:
	rm -rf bin
