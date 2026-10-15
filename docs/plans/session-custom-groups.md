# Plan: Sidebar session groups (MR #431)

## Summary
Separate user-defined session organization from working-directory grouping. Named groups have an optional default work directory, may contain sessions from different directories, and persist per authenticated user. Unassigned sessions continue to appear under their actual path. No cwd or runner changes result from grouping.

## Contract
- One explicit group per channel/chatID selector; membership uses the existing sessionKey helper.
- Show custom groups (including empty groups) before automatic path groups. Status/time views remain available.
- Group header offers independent edit and create controls; width stays within the sidebar.
- Editing an automatic path group creates a custom group and assigns its currently displayed, unassigned sessions atomically. Future sessions remain automatic.
- Global create defaults to automatic + recent directory; choosing a custom group uses its default directory, falling back to recents when empty. Directory remains editable.
- Fork inherits explicit group via the backend, without overriding fork cwd semantics.
- Drag a session onto a custom group or use its context-menu move action. Moving out clears membership and falls back to actual cwd. Never reinterpret a drop as a cwd change.
- Group header drag changes group order. Existing session reorder must not mix channels or accidentally alter unrelated groups.
- Delete group removes organization only, not sessions. Default-directory edits affect future creation only.
- Reuse current active-runner directory semantics; do not switch runner implicitly.

## Changes
- `channel/web/web_session_groups.go`: authenticated operation API, serialized server-owned per-user settings document, validation and revision ordering.
- `channel/web/web_rest.go`: fork membership inheritance.
- `web/src/lib/session-custom-groups.ts`, `web/src/hooks/useSessionGroups.ts`: types, grouping helpers, state synchronization and mutations.
- Session components and desktop built-in panel: group CRUD, conversion, create selector, menus and drag/drop; three-language labels.
- Unit/API/browser tests and `docs/agent/gotchas-web-frontend.md`.

## Risks
- Concurrent clients: operation-based serialized read/modify/write; reject stale client responses by revision.
- Unavailable backend or unreadable settings: surface failures; do not overwrite with empty state.
- Cross-channel identity: use channel+chatID everywhere; never use basename or chatID alone for membership.
- Paginated session lists: conversion includes only explicitly displayed sessions, never claims unseen history.
- Existing previews: backend additions need a separately running compatible test/preview backend; do not restart the existing backend without authorization.

## Definition of Done
- [x] CRUD, automatic conversion, ordered empty groups, per-user isolation, concurrent mutations covered by API tests.
- [x] Grouping, synchronization and stale response guards covered by frontend tests.
- [x] Create/Fork/move/delete preserve directory semantics and survive refresh.
- [x] Desktop/mobile edit and menu operations usable; drag grouping and group sorting verified.
- [x] Narrow sidebar has no horizontal overflow; full frontend tests/build/lint and relevant Go tests pass.
- [x] MR #431 contains only scoped changes and documents verified limitations.

## Open Questions
None blocking: user approved the one-level group UI, automatic-path conversion and edit button behavior. Implementation proceeds on the existing MR branch in an isolated worktree.

## Verification record
- Frontend: 183 files / 1,927 tests passed (`NODE_OPTIONS=--no-experimental-webstorage npm --prefix web test`).
- ESLint: 0 errors, 2 existing unused-disable warnings; TypeScript + Vite production build passed.
- Browser: 9 Chromium regressions passed (7 custom-group cases + 2 existing project-group cases), including native mobile touch and 220px geometry. Browser fixtures mock backend APIs; separate real-backend integration verified CRUD/move/reset/delete/Fork inheritance and settings privacy.
- Go: `go build ./...`; full suite passed with `TMPDIR=/tmp TZ=UTC GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=init.defaultBranch GIT_CONFIG_VALUE_0=master go test ./...`. These environment settings normalize existing macOS temp-path, UTC timestamp and default-branch assumptions without source changes. Relevant web/sqlite race tests passed.
- New draft-reset, cross-group sort contamination and selector projection regressions were verified failing before their fixes.
- Original 5174 preview and existing backend were not restarted. Isolated preview backend uses 8085 and its own database/configuration.
