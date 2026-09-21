# xbot-ssh-runner — Remote Machines (SSH) stdio plugin
#
# Zero-dependency Go stdio plugin. `make install` copies the binary + plugin.json
# into the xbot user plugin directory.
#
#   make build    → bin/ssh-runner-plugin
#   make vet      → go vet ./...
#   make test     → go test ./...
#   make install  → copy to ~/.xbot/plugins/xbot.ssh-runner/ (override with PLUGIN_DIR=...)

BINARY    := bin/ssh-runner-plugin
PLUGIN_ID := xbot.ssh-runner
PLUGIN_DIR ?= $(HOME)/.xbot/plugins/$(PLUGIN_ID)

.PHONY: build vet test install clean

build:
	@mkdir -p bin
	go build -o $(BINARY) .

vet:
	go vet ./...

test:
	go test ./...

install: build
	@mkdir -p $(PLUGIN_DIR)/bin
	cp $(BINARY) $(PLUGIN_DIR)/bin/
	cp plugin.json $(PLUGIN_DIR)/
	@echo "Installed to $(PLUGIN_DIR). Reload plugins to activate."

clean:
	rm -rf bin
