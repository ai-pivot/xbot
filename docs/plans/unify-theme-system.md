# Plan: 统一主题系统 — Markdown 主题为唯一主题

## Summary

当前有两套主题系统冲突：App 主题（light/dark via `<html class="dark">`）和 Markdown 主题（via `<html data-md-theme>`）。Markdown 主题的 `:root[data-md-theme]` 特异性 (0,2,0) 高于 `.dark` (0,1,0)，导致选了 Markdown 主题后 light/dark 开关失效。方案：让 Markdown 主题成为唯一主题，从中派生 dark/light，移除独立开关。

## Changes

### 1. `web/src/types/markdown-theme.ts`
- 给每个主题加 `mode: 'dark' | 'light'` 分类字段
- 移除 `auto` 选项（它是旧系统的桥接，不再需要）
- 默认值改为 `vscode-dark`

### 2. `web/src/providers/theme.tsx`
- 移除独立的 `theme` state 和 `setTheme`
- `theme` 改为从 `mdTheme` 派生：`const theme = MARKDOWN_THEMES.find(t => t.id === mdTheme)?.mode ?? 'dark'`
- 保留 `classList.toggle('dark', theme === 'dark')` 逻辑（Monaco/xterm/Sonner 仍需）
- `ThemeContextValue` 移除 `setTheme`，保留只读 `theme`（派生值）

### 3. `web/src/types/theme.ts`
- `ThemeContextValue` 移除 `setTheme`

### 4. `web/src/hooks/useTheme.ts`
- 无需改动（仍从 context 读 `theme`）

### 5. `web/src/layouts/ActivityBar.tsx`
- 移除 light/dark 切换按钮（太阳/月亮图标）
- 保留 Sessions 和 Settings 按钮

### 6. `web/src/components/settings/SettingsAppearance.tsx`
- 移除「主题」section（dark/light radio）
- 保留「主题色」（accent color）和「Markdown 主题」section
- 把 `useMarkdownTheme` 调用改为直接从 `useTheme()` 取 `mdTheme`/`setMdTheme`

### 7. `web/src/i18n/zh-CN.ts` + `en.ts`
- 移除 `mdThemeAuto` 翻译条目
- 保留 `settings.dark` / `settings.light`（其他组件可能仍引用）

### 8. `web/src/index.css`
- `:root` 和 `.dark` 的 `--md-*` 默认值保留（作为没有 `data-md-theme` 时的 fallback）
- `--bg-*` / `--border` / `--text-*` 的 `:root` 和 `.dark` 定义保留（`auto` 移除后不再需要，但保留不影响正确性，作为安全 fallback）

### 9. `web/src/styles/markdown-themes.css`
- 无需改动（已包含 `--bg-*` 覆盖）

### 10. `web/src/components/ui/sonner.tsx`
- `useTheme()` 仍返回 `theme`（派生值），无需改动

### 11. `web/src/workspace/panels/TerminalPanel.tsx`
- `useTheme()` 仍返回 `theme`（派生值），无需改动

### 12. `web/src/components/file/MonacoEditor.tsx`
- `useTheme()` 仍返回 `theme`（派生值），无需改动

## Risks
- **Monaco/xterm 配色**：派生的 `theme` 值决定 Monaco base (`vs-dark`/`vs`) 和 xterm 颜色。因为每个 Markdown 主题已声明 `mode`，派生值准确。
- **`auto` 移除**：已选 `auto` 的用户 localStorage 值会失效，fallback 到默认 `vscode-dark`。可接受（首次切换即可）。
- **MobileAppShell**：也引用 `useTheme()`，会自动获得派生 `theme` 值。

## Definition of Done
- [ ] Settings 外观面板只剩「主题色」+「Markdown 主题」，无 light/dark toggle
- [ ] ActivityBar 无太阳/月亮按钮
- [ ] 选择 GitHub Light → Monaco/xterm 使用浅色主题
- [ ] 选择 Dracula → Monaco/xterm 使用深色主题
- [ ] 刷新页面后主题保持（localStorage 持久化）
- [ ] build-and-sync 通过

## Open Questions
- 无
