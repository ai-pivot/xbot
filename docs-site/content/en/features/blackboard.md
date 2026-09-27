---
title: "Shared Blackboard"
weight: 45
---

# Shared Blackboard

A **board** is a durable workspace that several agents share. A main agent and
every SubAgent of its conversation already share one board automatically, and any
session can join a named board (`@my-board`) to coordinate across conversations.
The board answers the three failure modes of parallel agent work — with
mechanisms, not conventions:

| Failure mode | Mechanism |
|---|---|
| Two agents do the same work | **Atomic claim** (`Blackboard(action="claim")`) with a lease: a crashed worker's claim expires and the entry becomes available again — no sweeper, no deadlock |
| One agent overwrites another | **Revision CAS**: every write carries the revision it read; the loser gets a conflict *carrying the current entry* instead of clobbering it |
| Work starts before its input exists | **Dependencies** (`blocked_by`): an entry is `ready` only when every dependency is closed — and claiming a blocked entry is refused |

## Using it

```jsonc
// Plan: two entries, the second depends on the first
Blackboard(action="post", key="api-design", kind="task", title="Design /v2 API", body="…")
Blackboard(action="post", key="api-impl",   kind="task", title="Implement /v2 API", blocked_by=["api-design"])

// Find work — only "ready" entries are actionable
Blackboard(action="list", prefix="api-")

// Take it, report progress, finish (claim returns a claim_token for extend/release)
Blackboard(action="claim", key="api-impl")
Blackboard(action="update", key="api-impl", expected_revision=4, body="60% — endpoint sketch attached")
Blackboard(action="close",  key="api-impl", expected_revision=5)

// Be told instead of polling
Blackboard(action="watch")   // changes by others arrive as notifications; unwatch to stop
```

`kind` (`task` / `finding` / `decision` / `note` / anything you name) and `body`
are **opaque to the host**: the board stores and coordinates, it does not know
what a "task" is. That is what makes it a substrate for any collaboration
pattern rather than a fixed task schema.

## Event-driven, not polling

Watching is explicit and per session. When a watched board changes, the change is
delivered through the same background-notification pipeline as cron jobs and peer
messages: **busy agents get it injected into the current iteration, idle agents
are woken for a turn.** Bursts are coalesced into at most one notification per
board per 3 seconds, and a writer is never notified of its own change.

## In the Web UI

The **Blackboard** panel (left activity bar on desktop, the tools sheet on mobile)
shows the board of the session you are viewing: the derived counts (ready /
claimed / blocked / open), each entry's state, the lease countdown of whoever
holds it, its dependencies — and it updates live when *any* session changes the
board. You can also write on the board yourself: add an entry, close or reopen
one, release a stuck claim, or delete.

## Compared with Claude Code / Codex

| | Claude Code Agent Teams | Codex parallel agents | xbot Blackboard |
|---|---|---|---|
| Shared workspace | Task list | none (worktrees only) | generic, opaque payloads |
| Claiming | self-claim by **polling** | — | atomic + **lease** (self-healing) |
| Concurrency safety | file locking | — | revision **CAS** |
| Dependencies | yes | — | yes (claim refused while blocked) |
| Wake-up | none (poll) | none | **push** (busy → inject, idle → turn) |
| Durability | experimental; resumed teams lose members | — | SQLite (revisions, leases, deps survive restart) |
| Subagents participate | no | no | **yes** (main + all SubAgents share by default) |
