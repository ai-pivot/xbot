---
title: "Agent install guide"
weight: 11
---

# Agent install guide (executable)

> **For AI agents**: every command is runnable as-is and every result is assertable.
> The human-oriented version is [Install & configure](/installation/).
> The whole flow is verified on Debian 13 x86_64: install → serve → register → configure LLM → real reply.

## 0. Prerequisites

| Item | Requirement |
|---|---|
| OS | Linux / macOS (x86_64 / arm64); Windows uses `install.ps1` |
| Network | GitHub reachable (behind the GFW use the mirror below) |
| LLM | An OpenAI-compatible endpoint: `base_url` + `api_key` + `model` |

`base_url` must include the scheme and version path, e.g. `https://your-endpoint/v1`. **A wrong or empty
value fails the first chat with `Post "/chat/completions": unsupported protocol scheme ""`.**

## 1. Install

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
```

Behind the GFW (auto-selects a mirror):

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

Non-interactive / CI (works with no tty; everything via env vars):

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh \
  | MODE=standalone CHANNEL=stable PORT=8082 bash
```

There are **exactly 5 optional environment variables**:

| Variable | Values | Default |
|---|---|---|
| `MODE` | `standalone` (`xbot-cli serve` on demand) ｜ `server-client` (installs a persistent service) | `standalone` |
| `PORT` | Web UI + WebSocket port | `8082` |
| `XBOT_HOME` | Data directory | `~/.xbot` |
| `INSTALL_PATH` | Binary directory | `~/.local/bin` |
| `CHANNEL` | `stable`｜`beta`｜`nightly` | `stable` |

Internal variables: `GH_MIRROR` (CDN mirror), `NONINTERACTIVE=1` (skip all prompts).

**The script is idempotent**: re-running never clobbers existing settings (existing values always win).

Artifacts:

| Component | Location |
|---|---|
| CLI + server binary | `~/.local/bin/xbot-cli` |
| Config | `~/.xbot/config.json` |
| Web UI | `~/.xbot/web/dist` |
| Built-in plugins | `~/.xbot/plugins/builtin` |
| Database | `~/.xbot/xbot.db` |

Assert the install succeeded:

```bash
export PATH="$HOME/.local/bin:$PATH"
xbot-cli --version                    # prints the version
xbot-cli setup --check                # completeness check: prints "all good", exit 0; exit 1 if pieces are missing
ls ~/.xbot/web/dist/index.html        # Web UI in place
```

> If `setup --check` is non-zero, re-run `xbot-cli setup`. Missing plugin tarballs on an old release:
> reinstall with `CHANNEL=nightly`.

## 2. Start the web server

```bash
xbot-cli serve                        # foreground; port comes from config.json `web.port` (default 8082)
```

**To keep it running**, use the installer's server-client mode — it writes the systemd --user unit and
enables/starts it, so there is no hand-written unit file:

```bash
MODE=server-client bash install.sh    # service name is always xbot-server
```

Managing it:

```bash
systemctl --user status xbot-server
systemctl --user restart xbot-server
journalctl --user -u xbot-server -f
```

Assert it is up (**you must see 200**):

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/       # 200
```

> If a local proxy intercepts curl, add `--noproxy '*'`.
> Nothing listening? Check `config.json`: `web.enable` must be `true` and `web.port` must match.

## 3. Register the first account (no invite code)

The first registration is allowed while the `web_users` table is empty (bootstrap); the entry closes
afterwards.

```bash
curl -s -X POST http://127.0.0.1:8082/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<your-password>"}'
# → {"ok":true,"data":{"user_id":1},"error":null}
```

## 4. Configure the LLM

> ⚠️ **At runtime the database subscription (`user_llm_subscriptions`) is authoritative**; the `llm`
> section of `config.json` is only the **first-boot seed**. Editing `config.json` after install has no
> effect — unless you clear the seeded subscription so it re-seeds (below).

**4a. Web UI (recommended)**: log in → gear icon → **LLM** → add a subscription (`base_url` with
`https://…/v1` + `api_key`) → refresh the model list and pick a model → set as default → save.

**4b. Headless / scripted**:

```bash
python3 - <<'PY'
import json, os
p = os.path.expanduser("~/.xbot/config.json")
d = json.load(open(p))
d["llm"].update({"provider": "openai", "base_url": "https://your-endpoint/v1",
                 "api_key": "<your-key>", "model": "<your-model>"})
json.dump(d, open(p, "w"), indent=2, ensure_ascii=False)
PY
# Important: drop the already-seeded empty subscription so the next start re-seeds from it
sqlite3 ~/.xbot/xbot.db "DELETE FROM user_default_model; DELETE FROM user_llm_subscriptions;"
systemctl --user restart xbot-server      # foreground mode: Ctrl-C, then run `xbot-cli serve` again
```

## 5. Verify a chat

```bash
BASE=http://127.0.0.1:8082
curl -s -X POST $BASE/api/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<your-password>"}' -c /tmp/xbot.ck

curl -s -X POST $BASE/api/message -H 'Content-Type: application/json' -b /tmp/xbot.ck \
  -d '{"chat_id":"chat-check","content":"Introduce yourself in one sentence"}'
# → {"ok":true,"data":{"turn_id":1,"queued":false,...}}
```

**Where the reply text lives**: since v55 it is NOT in `session_messages.content` (that is an empty
placeholder row) but in **`iteration_history`**:

```bash
sqlite3 ~/.xbot/xbot.db \
  "SELECT iteration, substr(content,1,200), tokens, ttft_ms, model
     FROM iteration_history ORDER BY id DESC LIMIT 3;"
```

Success looks like: `1|I am xbot, an expert software engineer…|42|1984|macaron-v1-venti`
Failure: empty `content` plus `unsupported protocol scheme ""` in `detail` → the `base_url` from §4 is wrong.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing listening / web UI unreachable | `web.enable=false`, or `web.port` differs from the port in use | Fix `config.json`, or reinstall with `PORT=8082` |
| Chat fails with `unsupported protocol scheme ""` | subscription `base_url` is empty | Configure it per §4 (include `https://…/v1`) |
| Register returns `registration is invite-only` | an account already exists | Log in as the existing admin, or invite from the web UI |
| `setup --check` reports a component MISSING | missing pieces | `xbot-cli setup`; offline: `--offline-web/--offline-plugins` |
| Editing `config.json` changed nothing | the subscription is already seeded; runtime reads the DB | See 4b (clear the subscription, restart) |
| `does not support the setup subcommand` | installed a release older than `setup` | Reinstall with `CHANNEL=nightly` |
| `command not found: xbot-cli` | `~/.local/bin` is not on PATH | `source ~/.bashrc` or reopen the terminal |

## Uninstall

```bash
systemctl --user disable --now xbot-server 2>/dev/null || true
rm -f ~/.config/systemd/user/xbot-server.service
rm -f ~/.local/bin/xbot-cli
rm -rf ~/.xbot          # data directory — includes the database
```
