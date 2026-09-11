---
title: "Installation"
weight: 10
---

# Install & configure

## One command

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

That is the whole install: **binary + Web UI + every built-in plugin (genui / git-fancy /
ambience), with the web channel already enabled.** Nothing else to install, and no config
file to edit first.

```bash
xbot-cli serve          # then open http://localhost:8082
```

In the browser: create an account → gear icon → **LLM** → base URL / API key / pick a
model → start chatting.

{{< hint type=note >}}
The first account on a fresh install needs no invite code and becomes the admin.
{{< /hint >}}

## Let your agent install it

Paste this into your AI agent (Claude Code / Codex / Cursor …) — it installs and
verifies on its own:

```text
Install and start xbot on this machine:

1. Install: curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
2. Verify:  xbot-cli setup --check        # exit code 0 means the install is complete
3. Start:   nohup xbot-cli serve >/tmp/xbot.log 2>&1 &
4. Confirm: curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8082   # must print 200

If any step fails, read the Troubleshooting section below — do not skip the
check and claim it worked.
```

Notes for the agent:

- `install.sh` is **idempotent** — re-running it never clobbers existing settings
  (existing values always win).
- `setup --check` is the single source of truth for a complete install: exit code 0
  means all checks passed.
- `serve` runs in the foreground; background it yourself (`nohup` / `systemd` / `tmux`).
- After install `~/.xbot/config.json` already has the web + plugin settings. **Do not
  hand-edit `llm.*`** — LLM settings live in the database; use the web settings panel
  or the `xbot-cli` commands instead.

## Optional flags (all 5 of them)

Environment-variable prefixes — put them before the command, e.g.
`MODE=server-client PORT=9000 bash install.sh`:

| Variable | Default | Purpose |
|----------|---------|---------|
| `MODE` | `standalone` | `standalone` = `xbot-cli serve` on demand; `server-client` = install a persistent service (systemd --user / launchd) that the CLI connects to |
| `PORT` | `8082` | Web UI + WebSocket port |
| `XBOT_HOME` | `~/.xbot` | Data directory (config, database, plugins, web assets) |
| `INSTALL_PATH` | `~/.local/bin` | Where the binary is installed |
| `CHANNEL` | `stable` | `stable` / `beta` / `nightly`; `nightly` is the latest build, overwritten on every master push |

Behind the GFW, use the mirror (`GH_MIRROR` is set automatically by the mirror script):

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

**Everything else** — LLM subscriptions, channels (Feishu / QQ / Web / CLI), sandbox and
runners, memory, hooks, logging, plugins — lives in
[Configuration](/configuration/).

## Verify

```bash
xbot-cli --version        # version
xbot-cli setup --check    # completeness check: exit code 0 = OK
```

```bash
# Is the web server up? (add --noproxy '*' if a local proxy intercepts curl)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082     # expect 200

# server-client mode service status
systemctl --user status xbot-server     # Linux
launchctl list | grep xbot              # macOS
```

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `setup --check` reports a component MISSING | Re-run `xbot-cli setup`; offline: pass `--offline-web/--offline-plugins` with local packages |
| Page loads but sending fails with `unsupported protocol scheme ""` | LLM not configured. Web → gear → LLM, enter base URL + API key + pick a model (editing `config.json` does nothing) |
| Port already in use / want another port | `PORT=9000 xbot-cli serve`, or change `web.port` and restart |
| Page 404s or renders unstyled | Web assets missing: `xbot-cli setup` |
| Plugin panels are blank | Plugins not activated: `xbot-cli setup --config-only` or `/plugin reload-all` |
| `command not found: xbot-cli` | `~/.local/bin` is not on PATH: `source ~/.bashrc` or reopen the terminal |
| An old release installed without plugins | `xbot-cli setup`; if that fails, reinstall with `CHANNEL=nightly` (nightly always ships the plugin tarball) |

## Upgrade

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
```

Re-running the installer is the upgrade path: the binary is replaced, `config.json`
and the database are left untouched.

## Uninstall

```bash
systemctl --user disable --now xbot-server   # server-client mode
rm -f ~/.local/bin/xbot-cli
rm -rf ~/.xbot                                # data dir — includes the database
```

## Build from source

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make setup      # build CLI + Web UI + built-in plugins, install into ~/.xbot, activate channels
```

Requires **Go 1.26+**; `make setup` additionally needs Node.js (web frontend build).

## See also

- [Configuration](/configuration/) — full `config.json`, LLM subscriptions, model tiers
- [Getting started](/getting-started/) — your first conversation after install
- [Channels](/channels/) — Feishu / QQ / Web / CLI setup
- [Plugins](/plugins/) — plugin system and built-in plugins
