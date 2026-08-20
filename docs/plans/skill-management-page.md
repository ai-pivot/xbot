# Plan: Skill Management Panel

## Summary

Add a right-sidebar panel for managing skills — list all discovered skills (embedded/global/user/project) with source badges, toggle enable/disable, view SKILL.md content, and install/uninstall user skills. Uses REST endpoints (not RPC). Lives in the right sidebar (like files/search/info/tasks/terminal), not in SettingsDialog.

## Design Decisions

1. **REST, not RPC** — Web frontend uses `postAPI('/api/...')` for all new features. No `conn.rpc` wrappers, no `req_types.go`/`client.go` changes.
2. **Right sidebar panel** — Add a "skills" panel to `RightSidebar` (alongside files/search/info/tasks/terminal). Add a `Sparkles`/`BookOpen` icon to `RightActivityBar`. Does NOT touch `SettingsDialog`.
3. **Show skill type** — Each skill shows a source badge: `embedded` / `global` / `user` / `project`.
4. **Include project-level skills** — `ListSkills()` only returns 3 tiers (embedded/global/user). New `ListSkillsDetailed()` also calls `scanProjectSkills` to include project-local skills.
5. **Enable/disable = global** — Modifies `config.json` `disabled_skills`. Matches existing architecture.
6. **Reuse existing install/uninstall** — `/api/app/install-file` (multipart) + `/api/app/uninstall` already exist.

## Changes

### Backend

#### `agent/skills.go`
- Add `SkillDetail` struct — embeds `SkillInfo` + `Source string` (embedded/global/user/project) + `Enabled bool` + `CanUninstall bool`.
- Add `ListSkillsDetailed(ctx, senderID, projectDir) ([]SkillDetail, error)` — calls `ListSkills` + `scanProjectSkills`, derives `Source` from `Path`, `Enabled` from `isDisabled`, `CanUninstall` from source==user.
- Add `SetSkillEnabled(name string, enabled bool)` — toggles `disabledSkills` map + `InvalidateCache()`.
- Add `GetSkillContent(name string) (string, error)` — resolves skill dir, reads SKILL.md.

#### `channel/web/web.go`
- Register 3 REST routes: `/api/skills/list`, `/api/skills/toggle`, `/api/skills/content`.

#### `channel/web/web_rest.go`
- `handleSkillsListPOST` — calls `wc.rpcCall("skill_list", ...)` or accesses agent via callback. Returns `[]SkillDetail`.
- `handleSkillsTogglePOST` — body `{name, enabled}`. Toggles + persists config.
- `handleSkillsContentPOST` — body `{name}`. Returns SKILL.md content.

#### `serverapp/rpc_table.go`
- Add `skill_list`, `skill_set_enabled`, `skill_get_content` RPC handlers (internal dispatch from REST handlers via `wc.rpcCall`, same pattern as `app_install_file`).

### Frontend

#### `web/src/components/sidebar/RightSidebar.tsx`
- Add `'skills'` to `SidebarPanel` type.
- Add `SkillsPanel` to `renderPanel` switch + `titleFor`.

#### `web/src/components/sidebar/RightActivityBar.tsx`
- Add `{ panel: 'skills', icon: Sparkles, labelKey: 'sidebar.skills' }` to `PANELS` array.

#### `web/src/components/sidebar/SkillsPanel.tsx` (new)
- Skill list: each row = name + description + source badge + enable/disable Switch + view button + uninstall button (user skills only).
- Install section: file upload (POST `/api/app/install-file`) + URL input.
- View: opens SKILL.md content in a Dialog (markdown rendered).
- Uses `postAPI` for all calls. No `conn.rpc`.

#### `web/src/lib/api.ts` or inline in component
- `listSkills()` → `postAPI('/api/skills/list')`
- `toggleSkill(name, enabled)` → `postAPI('/api/skills/toggle', {name, enabled})`
- `getSkillContent(name)` → `postAPI('/api/skills/content', {name})`

#### `web/src/i18n/en.ts` + `zh-CN.ts`
- Add `sidebar.skills` + skill-related labels (source badges, actions, install, view modal).

## Risks

- **Config persistence**: `SaveToFile` deep-merges. Concurrent toggles could lose entries. Mitigation: serialize in the RPC handler (agent loop is per-session serialized).
- **`GetSkillContent` path resolution**: `SkillTool.resolveSkill` is on the tool struct. Need to extract resolution logic into `SkillStore` or duplicate.
- **Cache staleness after install**: `installAppSkill` doesn't call `InvalidateCache`. Mitigation: call `InvalidateCache` after install.
- **Project skills are CWD-dependent**: `scanProjectSkills` needs a `projectDir`. The REST handler can derive it from the session CWD or pass empty (project skills optional).

## Definition of Done

- [ ] `GET /api/skills/list` returns all skills with source/enabled/canUninstall
- [ ] `POST /api/skills/toggle` toggles a skill and persists to config.json
- [ ] `POST /api/skills/content` returns SKILL.md content
- [ ] Right sidebar has a "Skills" panel (icon in RightActivityBar)
- [ ] Each skill row shows name, description, source badge, enable/disable toggle
- [ ] View button opens a modal showing SKILL.md content
- [ ] Install via file upload works (reuses `/api/app/install-file`)
- [ ] Uninstall button works for user-installed skills (reuses `/api/app/uninstall`)
- [ ] Toggling enable/disable reflects immediately (InvalidateCache called)
- [ ] i18n keys added for both en and zh-CN
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes
- [ ] Frontend builds (`npm run build` in `web/`)
