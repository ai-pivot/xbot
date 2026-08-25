.PHONY: fmt lint test build run dev clean ci clean-memory web-build web-lint web-dev install-cli plugins-build plugins-install plugins-clean

BINARY_NAME := xbot

# Worktree-safe: go build's VCS stamping fails in git worktrees
# (it cd's to the main repo which is locked by the worktree).
# Override with: make install-cli GOFLAGS=
GOFLAGS ?= -buildvcs=false

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

test:
	go test -v -race -coverprofile=coverage.out ./...

VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
CHANNEL := $(shell git branch --show-current 2>/dev/null | sed 's/master/stable/' | sed 's/.*/stable/' | head -1)
LDFLAGS := -X xbot/version.Version=$(VERSION) -X xbot/version.Commit=$(shell git rev-parse --short HEAD) -X xbot/version.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) -X xbot/version.Channel=$(CHANNEL)

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) .

run: build
	./$(BINARY_NAME)

dev:
	go run $(GOFLAGS) -ldflags "$(LDFLAGS)" .

clean:
	rm -f $(BINARY_NAME) coverage.out
	go clean

ci: lint build test web-lint web-build
	@echo "CI checks passed!"

clean-memory:
	rm -rf .xbot/
	@echo "Memory cleaned!"

web-build:
	cd web && yarn build

web-lint:
	cd web && yarn lint

web-dev:
	cd web && yarn dev

install-cli:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o /tmp/xbot-cli ./cmd/xbot-cli
	sudo mv /tmp/xbot-cli /usr/local/bin/

# ── Built-in plugins (repo plugins/) ─────────────────────────────────────────
# Two ways to use them:
#   1. Install into ~/.xbot/plugins/ (production-style):
#        make plugins-install
#   2. Run directly from the repo checkout (development):
#        make plugins-build
#        XBOT_PLUGIN_DIRS="$(CURDIR)/plugins" ./xbot
plugins-build:
	$(MAKE) -C plugins/xbot-genui build
	$(MAKE) -C plugins/xbot-git-fancy build

plugins-install: plugins-build
	$(MAKE) -C plugins/xbot-genui install
	$(MAKE) -C plugins/xbot-git-fancy install
	@echo "Builtin plugins installed to $$HOME/.xbot/plugins/. Reload to activate: tui_control(action=reload_plugins)"

plugins-clean:
	$(MAKE) -C plugins/xbot-genui clean
	$(MAKE) -C plugins/xbot-git-fancy clean

