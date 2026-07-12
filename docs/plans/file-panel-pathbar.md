# Plan: 文件面板地址栏 — 切换浏览根目录

## Summary

在右侧文件面板（FileExplorer）顶部加一个地址栏输入框 + 重置按钮。用户输入路径后，用 `statFile` API 校验：是目录则切换浏览根到该目录，是文件则切换到其父目录，不存在则不变。重置按钮回到当前 session 的 CWD。

## Changes

### `web/src/components/sidebar/FileExplorer.tsx`
- What: 在文件树上方新增一个 `PathBar` 子组件（输入框 + 重置按钮），维护一个本地 `browseRoot` state。地址栏默认显示当前 session CWD。提交时调 `statFile` 校验路径，成功则 `setBrowseRoot(targetDir)` 并让 `useFileTree` 用新根重新加载。
- Why: 当前 FileExplorer 完全依赖 `useCwd()` 作为根目录，无法临时浏览其他位置。

### `web/src/hooks/useFileTree.ts`
- What: 新增可选 `rootPath` 参数（`useFileTree(rootPath?: string)`）。如果传了 `rootPath`，用它替代 `cwd` 作为树根；不传则回退到 `cwd`（现有行为不变）。
- Why: 让 FileExplorer 能控制浏览根，不影响其他依赖 `useFileTree()` 的组件。

### `web/src/hooks/useFileSystem.ts`
- What: 无改动。已有 `statFile` 和 `parentPath` 可直接使用。

### `web/src/i18n/zh-CN.ts` + `en.ts`
- What: 新增 `sidebar.pathBarPlaceholder` / `sidebar.pathBarReset` / `sidebar.pathNotFound` 翻译。
- Why: 地址栏的 placeholder、按钮 tooltip 和错误提示。

## 流程

```
用户输入路径 → 按 Enter → statFile(path)
  ├─ 200 + isDir=true  → setBrowseRoot(path) → useFileTree 重新加载
  ├─ 200 + isDir=false → setBrowseRoot(parentPath(path)) → useFileTree 重新加载
  └─ 404/错误          → toast 提示路径不存在，地址栏不变
```

## Risks
- **useFileTree root 参数**：其他调用方（FileSearch）也用 `useFileTree()`，不传参数时行为不变，无风险。
- **路径校验**：`statFile` 返回 404 时 fetch 会 throw，catch 后提示即可。
- **缓存**：`listDir` 有 30s 缓存，切换目录后可能看到旧数据。在 `setBrowseRoot` 时调 `invalidateFsCache()` 清缓存。

## Definition of Done
- [ ] 文件面板顶部有地址栏，显示当前浏览根目录
- [ ] 输入存在的目录路径 → Enter → 文件树切换到该目录
- [ ] 输入文件路径 → Enter → 切换到文件所在目录
- [ ] 输入不存在的路径 → Enter → 提示不变
- [ ] 点击重置按钮 → 回到 session CWD
- [ ] build-and-sync 通过

## Open Questions
- 无
