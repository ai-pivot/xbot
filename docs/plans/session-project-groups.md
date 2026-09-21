# Plan: 会话列表按项目组织（项目分组可折叠）

## Summary

会话列表**默认按项目（= 工作目录）分组**，组头可折叠/展开，折叠状态按项目持久化（localStorage），并提供「全部折叠/展开」。
现状：`time|status|path` 三分类里 `path` 已经按 `workDir` 分组、`SessionGroup` 组头也已可折叠，但 ① 桌面 `core.sessions` 面板**没有分类切换器**（只有手机抽屉有）⇒ 桌面切不到按项目；② 默认分类是 `time`；③ 折叠是组件内 `useState`，刷新/重挂载即复位，且没有「全部折叠/展开」。

## Changes

### `web/src/lib/session-grouping.ts`
- What: 新增 `SESSION_CATEGORIES`（唯一顺序来源）、`DEFAULT_SESSION_CATEGORY = 'path'`、`isSessionCategory()`、`collapseKey(category, groupKey)`（折叠状态键的唯一实现）。
- Why: 分类顺序/默认值/折叠键在面板与抽屉两处共用，禁止各写一份（历史事故：CLI 与 serverapp 各维护一份 schema 导致漂移）。

### `web/src/hooks/useSessionStore.ts`
- What: `loadCategory()` 默认改为 `DEFAULT_SESSION_CATEGORY`；对**历史遗留的 `'time'`**（旧默认值）做一次性迁移（标记 `xbot:session-category-migrated`，迁移后写回 server）；新增折叠状态 `collapsedGroups: string[]`（持久化 key `xbot:session-collapsed-groups`）+ `toggleGroupCollapsed(key)` + `setGroupsCollapsed(keys, collapsed)`。
- Why: 默认按项目组织；折叠状态必须跨刷新/跨面板实例一致。「全部折叠/展开」需要批量写入口。

### `web/src/components/session/SessionGroup.tsx`
- What: 由内部 `useState(true)` 改为**受控** `open` + `onToggle`；组头内容保持「项目名 + 会话数」（用户要求：不加额外按钮），保留完整路径 tooltip 以消歧同名目录。
- Why: 状态要提升到 store 才能持久化 + 全局折叠。

### `web/src/components/session/SessionViewBar.tsx`（新增）
- What: 分类分段控件（项目/状态/时间）+ 右侧「全部折叠/展开」图标按钮；props = 当前分类的 group keys。桌面面板与手机抽屉**共用同一组件**。
- Why: 桌面缺失分类切换器是「渠道下拉只在手机里」同类遗漏；统一组件避免两份实现漂移。

### `web/src/components/session/SessionList.tsx`
- What: 新增 `collapsedGroups` + `onToggleGroup` props，按 `collapseKey(category, g.key)` 计算每个组的 `open` 并下发。
- Why: 折叠状态的渲染侧接线。

### `web/src/components/panel/builtinPanels.tsx` / `web/src/components/session/SessionSidebar.tsx`
- What: 桌面 `core.sessions` 与手机抽屉都渲染 `SessionViewBar`；抽屉删除内联分类切换（去重）；两者都把折叠状态传给 `SessionList`。
- Why: 平台一致（桌面可切换分类）+ 单一实现。

### i18n `web/src/i18n/{zh-CN,en,ja}.ts`
- What: `session.byPath` 值改为「项目 / Projects / プロジェクト」；新增 `session.collapseAllGroups` / `session.expandAllGroups`（⚠️ `collapseAll` / `expandAll` 是旧「折叠级别」特性的禁词，`noLegacyFoldFormat.test.tsx` 的 `FORBIDDEN_CODE`/`DEAD_KEYS` 会红）。
- Why: 用户口径是「项目」；三语必须同步（有守卫测试）。

### 测试
- `session-grouping.test.ts`：`collapseKey` / `SESSION_CATEGORIES` / `DEFAULT_SESSION_CATEGORY` / `isSessionCategory`。
- `useSessionStore.test.ts`：默认项目分组 / 遗留 `time` 只迁移一次（显式选 time 后不再被改）/ 折叠持久化 / toggle / 全部折叠。
- `SessionList.test.tsx`：组头点击折叠、受控 open 渲染。
- `SessionViewBar.test.tsx`（新增）：分类切换回调 / 全部折叠与展开的判定与回调。
- `builtinPanels.*.test.tsx`：桌面面板必须渲染分类切换器（回归守护）。
- `web/e2e/session-project-groups.spec.ts`（新增）：默认按项目分组 + 点组头折叠 + 刷新保持 + 全部折叠/展开。

## Risks
- 默认分类变更遇到历史 `time`：一次性迁移（有标记），迁移后写回 server，之后显式选择生效；避免"切换器对 time 失效"。
- `SessionGroup` 由非受控变受控：漏传 props 会导致组不可折叠 —— props 设为必填，tsc 会挡住。
- 折叠键含 category：三个分类各自记忆，互不干扰（项目维度即"按项目记住"）。
- `SessionList` 未虚拟化：全部折叠只减少 DOM，无性能风险。

## Definition of Done
- [ ] 无历史值时，列表默认按项目分组
- [ ] 桌面左栏 + 手机抽屉都能切换 项目/状态/时间
- [ ] 点项目组头折叠/展开；刷新后保持；「全部折叠/展开」可用
- [ ] 组头只有 项目名 + 会话数（无额外按钮），完整路径保留在 tooltip
- [ ] `tsc -b` + `vitest` 全绿（无 Go 改动）
- [ ] 新增 E2E spec（CI 跑）
- [ ] AGENTS.md / docs-site 同步

## Open Questions
- 无（四项设计已由用户确认）
