.PHONY: fmt lint test build run dev clean ci clean-memory web-build web-lint web-dev install-cli plugins-build plugins-install plugins-install-builtin plugins-clean plugins-web plugins-package setup

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
#
# Note: the RELEASE pipeline packages plugins via plugins/package.sh (see
# `make plugins-package` and .github/workflows/release.yml). Installing to
# ~/.xbot/plugins/ takes precedence over ~/.xbot/plugins/builtin/ (the
# release-installed copies) — discovery dedups by plugin ID, user dir first.
XBOT_HOME ?= $(HOME)/.xbot

# Build plugin web assets (git-fancy ESM views) with esbuild from the main
# web source tree. Requires web/node_modules (npm ci in web/). Same command
# the release CI uses; output goes to the plugin's source web/ dir (gitignored).
plugins-web:
	cd web && npx esbuild src/plugins/git-fancy/index.tsx src/plugins/git-fancy/commit.tsx \
		--bundle --splitting --format=esm --jsx=transform \
		--outdir=../plugins/xbot-git-fancy/web

plugins-build:
	$(MAKE) -C plugins/xbot-genui build
	$(MAKE) -C plugins/xbot-git-fancy build

plugins-install: plugins-build plugins-web
	$(MAKE) -C plugins/xbot-genui install
	$(MAKE) -C plugins/xbot-git-fancy install
	# git-fancy web assets (esbuild bundles built by plugins-web — Makefile
	# plugins/ dirs are the ONLY consumers; the release pipeline ships them
	# via plugins/package.sh --web-dist-dir instead)
	mkdir -p $(XBOT_HOME)/plugins/xbot.git-fancy/web
	cp -R plugins/xbot-git-fancy/web/. $(XBOT_HOME)/plugins/xbot.git-fancy/web/
	# ambience: script-runtime plugin, manifest only (frontend builtin handles the rest)
	mkdir -p $(XBOT_HOME)/plugins/xbot.ambience
	cp plugins/xbot-ambience/plugin.json $(XBOT_HOME)/plugins/xbot.ambience/
	@echo "Builtin plugins installed to $(XBOT_HOME)/plugins/ (user dir — takes precedence over builtin/)."
	@echo "Dev override: these copies win over release-installed ~/.xbot/plugins/builtin/."

# Install the built-in plugins into the RELEASE-MANAGED dir (plugins/builtin/)
# — the same place `xbot-cli setup` installs release tarballs. `setup
# --config-only` (channel activation) only scans builtin/, so `make setup`
# installs here; use plain `make plugins-install` for the user-dir dev
# override instead.
plugins-install-builtin: plugins-build plugins-web
	$(MAKE) -C plugins/xbot-genui install PLUGIN_DIR='$(XBOT_HOME)/plugins/builtin/xbot.genui'
	$(MAKE) -C plugins/xbot-git-fancy install PLUGIN_DIR='$(XBOT_HOME)/plugins/builtin/xbot.git-fancy'
	mkdir -p $(XBOT_HOME)/plugins/builtin/xbot.git-fancy/web
	cp -R plugins/xbot-git-fancy/web/. $(XBOT_HOME)/plugins/builtin/xbot.git-fancy/web/
	mkdir -p $(XBOT_HOME)/plugins/builtin/xbot.ambience
	cp plugins/xbot-ambience/plugin.json $(XBOT_HOME)/plugins/builtin/xbot.ambience/
	@echo "Built-in plugins installed to $(XBOT_HOME)/plugins/builtin/ (release-managed dir)."

plugins-clean:
	$(MAKE) -C plugins/xbot-genui clean
	$(MAKE) -C plugins/xbot-git-fancy clean
	rm -rf plugins/xbot-git-fancy/web

# Package plugins into per-platform tarballs (release-style) for local testing.
# Produces dist/xbot-plugins-<os>-<arch>.tar.gz — the same artifacts the
# release CI publishes (release.yml build-plugins job uses --web-dist-dir with
# a staging layout; locally the source-tree web/ dir from plugins-web is used).
plugins-package: plugins-web
	bash plugins/package.sh --out dist --version $(VERSION)

# One-command local setup for source checkouts: build CLI + web dist +
# plugins, install everything to XBOT_HOME, activate channel plugins.
#   - web dist      → $(XBOT_HOME)/web/dist (resolveStaticDir finds it there)
#   - plugins       → $(XBOT_HOME)/plugins/builtin/ (release-managed dir —
#                    setup --config-only only scans builtin/ for channel
#                    activation; use `make plugins-install` for the user-dir
#                    dev override instead)
#   - channels config fixup via ./xbot-cli setup --config-only
setup: web-build plugins-install-builtin
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o xbot-cli ./cmd/xbot-cli
	mkdir -p $(XBOT_HOME)/web/dist
	cp -R web/dist/. $(XBOT_HOME)/web/dist/
	./xbot-cli setup --config-only
	@echo ""
	@echo "Setup complete:"
	@echo "  binary:   ./xbot-cli (dev build)"
	@echo "  web dist: $(XBOT_HOME)/web/dist"
	@echo "  plugins:  $(XBOT_HOME)/plugins/builtin/"
	@echo "  Start the server: ./xbot-cli serve"

