---
title: "Plugin Cookbook"
weight: 1
geekdocCollapseSection: true
---

Hands-on recipes for building xbot plugins. Every recipe starts with a working example drawn from the real plugin code in this repository (`plugin/examples/`, `plugins/xbot-genui`, `plugins/xbot-git-fancy`) and explains the API behind it.

## What you will learn

| Recipe | What you build |
|---|---|
| [Quick Start: Script Plugin](./quick-start-script/) | A `git-info` status widget in bash — no compilation |
| [Quick Start: Go Plugin](./quick-start-go/) | A native Go plugin with tools, hooks, and a context enricher |
| [Quick Start: Stdio Plugin](./quick-start-stdio/) | A Python plugin speaking NDJSON over stdio |
| [Quick Start: Web Plugin](./quick-start-web/) | A frontend ESM view panel |
| [Script Plugins](./script-plugins/) | Full guide: widgets, env vars, triggers, sync hints |
| [Go Plugins](./go-plugins/) | Full guide: `PluginContext`, SDK helpers, UI bridges |
| [Stdio Plugins](./stdio-plugins/) | Full guide: protocol handlers in any language |
| [Channel Plugins](./channel-plugins/) | Full channel adapters: tools, prompts, web UI |
| [Web Plugins](./web-plugins/) | Type-as-contract frontend plugins with `@xbot/plugin-api` |
| [Configuration](./configuration/) | Declarative plugin settings with defaults |
| [Widgets](./widgets/) | Status bar, info bar, and footer widgets |
| [Hooks](./hooks/) | Lifecycle events: deny, ask, defer, allow |
| [Tools](./tools/) | Registering tools the agent can call |
| [Event Bus](./event-bus/) | Plugin-to-plugin pub/sub |
| [Storage](./storage/) | Persistent per-plugin key-value state |
| [Permissions](./permissions/) | The complete permission catalogue |
| [Dependencies](./dependencies/) | Plugin dependency graphs and activation order |
| [Debugging](./debugging/) | Logs, profiler, hot reload |
| [Testing](./testing/) | `TestKit`, mocks, and golden tests |
| [Publishing](./publishing/) | Distribution, checksums, migration |

## The five-minute path

New to xbot plugins? Follow the four Quick Start recipes in order. They cover the four runtimes supported by `plugin.NewCompositeRuntimeFactory()` (`plugin/runtime_factory.go`):

```go
switch manifest.Runtime {
case RuntimeNative:  // in-process Go
case RuntimeGRPC, RuntimeStdio: // external NDJSON process
case RuntimeScript: // periodic external script
}
```

Each runtime targets a different trade-off: **script** for zero-build widgets, **native Go** for tight integration, **stdio** for any language, and **web** for UI-only frontend extensions. A single `plugin.json` can combine them — see the built-in `xbot.git-fancy` plugin, which pairs a Go stdio backend with a React view.

## Where plugins live

```
~/.xbot/plugins/<plugin-id>/
├── plugin.json      # manifest — id, runtime, entry, permissions, contributes
├── <entry file>     # main.sh / main.go / main.py / web/index.js
└── data/            # runtime storage (created automatically)
```
