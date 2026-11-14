# Runbook：遗留 `.xbot-worktrees/` 目录与陈旧会话 CWD 的人工清理

> 背景：Worktree 工具已于 2026-10-27 **整体删除**（`3668b4a1` / `6adbb657`，
> 用户令："worktree 工具全是问题，工具与相关 prompt 一并删除"）。
> 代码里不再有任何东西创建、注册、读取这些目录与 `registry.json`，但**磁盘和主库
> 里还留着现场**。本文件是**人工**处置步骤。
>
> ⛔ **硬规则：不许自动 prune，不许写脚本批量删。**
> 其中至少一个目录是别的会话**正在使用的工作树**，自动化只会把别人的工作删掉。

## 1. 现状（2026-10-27 实测）

磁盘 `/home/smith/src/.xbot-worktrees/`：

| 目录 | 登记情况（`git worktree list` 实测） | 处置 |
|---|---|---|
| `port-v0521-taizi` | ✅ 登记在 `sglang-b300-glm52`：`port/upstream-v0521-taizi @ e90fcfe2ef` | ⛔ **仍被另一个会话占用，本次不动** |
| `glm53flash-mm-kv` | ✅ 登记在 `sglang-b300-glm52`：`glm53flash-b300 @ 40d14d672a` | 有分支、有历史 ⇒ 先问 owner，**不是垃圾** |
| `hicache-repair` | ✅ 登记在 `sglang-b300-glm52`：`fix/hicache-dcp-storage-lifecycle @ 91a771b0f8` | 有分支 ⇒ 先问 owner |
| `peer-rw7ms-7ms-structural-rewrite` | ✅ 登记在 `ferrite-v2`：`agent/peer/rw7ms/7ms-structural-rewrite @ 5cb025dd` | 有分支 ⇒ 先问 owner |
| `child-pg1-fused-wire-flashinfer-pg1-fused-moe` | ✅ 登记在 `ferrite-v2`：`agent/child/pg1-fused-wire/flashinfer-pg1-fused-moe @ 1a106e8c` | 有分支 ⇒ 先问 owner |
| `peer-web-perf-web-performance-optimization--` | ❌ 无 `.git`、无登记 | **唯一确认的死目录**（内容已无从恢复定位，删前先看有无未导出文件） |
| `registry.json` | — | 无代码再读它，最后删 |

⚠️ 结论：**6 个目录里 5 个是登记在册、挂着真实分支的工作树**。谁都不是"因为工具删了就该清"的垃圾 ——
工具删除只意味着**不会再有新的**，存量必须按第 3 节逐个确认 owner 后再动。

主库 `xbot.db` 里有 **10 条 tenant** 的 `cwd` 落在该目录（只读查询）：

```bash
python3 -c "
import sqlite3, os
c = sqlite3.connect('file:/home/smith/.xbot/xbot.db?mode=ro', uri=True)
for r in c.execute(\"select id,channel,chat_id,cwd from tenants where cwd like '%xbot-worktrees%'\"):
    print(r[0], r[1], r[2], os.path.isdir(r[3]), r[3])
"
```

- `id=178477`（`web:chat_881A844DB401`）指向的目录**已不存在**；
- 其余 9 条的目录仍在（3 个 web 会话 + 6 个 SubAgent 会话）。

## 2. 第一步：DB 里的陈旧 CWD —— 不要手工 UPDATE

部署带 **stale CWD 守卫**的版本后（`agent/stale_cwd.go` 的 `resetStaleCWD`，
判据只是 `dirExists(cwd)`，与工具无关），会话**下一个 turn** 就会：

- 目录不存在 ⇒ 自动复位到 `workspaceRoot` 并打 WARN（`Persisted session CWD is
  inaccessible, resetting to workspace root`），Shell 立刻恢复可用；
- 目录存在 ⇒ 原样保留（**刻意不碰**：用户可能仍在该目录里干活）。

所以：

- ⛔ 不要写 `UPDATE tenants SET cwd=...` 批量刷库 —— 会把"用户其实还在用"的目录也刷掉，
  且绕过日志；
- ✅ 部署后复查：上面那条查询里 `exists=False` 的记录应在该会话跑过一轮后归零：

```bash
# 只看落点的统计（仍然是只读）
python3 -c "
import sqlite3, os
c = sqlite3.connect('file:/home/smith/.xbot/xbot.db?mode=ro', uri=True)
rows = list(c.execute(\"select cwd from tenants where cwd like '%xbot-worktrees%'\"))
print('total:', len(rows), 'missing:', sum(0 if os.path.isdir(r[0]) else 1 for r in rows))
"
```

## 3. 第二步：磁盘目录清理（逐仓库核对，逐个人工删）

**顺序不能反：先按仓库 `git worktree list` 核对，再删。**

```bash
# 3.1 该仓库现在登记了哪些工作树（每个目录的 .git 里写着它属于哪个仓库）
cat /home/smith/src/.xbot-worktrees/<dir>/.git      # → gitdir: /home/smith/src/<repo>/.git/worktrees/<dir>
cd /home/smith/src/<repo> && git worktree list      # 核对 <dir> 是否仍登记在册

# 3.2 确认没有会话在用：目录 mtime 是否近 24h 有变动、是否有未提交改动
cd /home/smith/src/.xbot-worktrees/<dir> && git status --short
#    有改动 ⇒ 先导出补丁再删：git diff > /tmp/<dir>.patch

# 3.3 用 git 删工作树（不要 rm -rf，那会留下 .git/worktrees 里的孤儿登记）
cd /home/smith/src/<repo> && git worktree remove /home/smith/src/.xbot-worktrees/<dir>
#    git worktree remove 拒绝执行时不加 --force，先回去看 3.2 的输出

# 3.4 收尾：确认登记已消失，再删孤儿登记目录
cd /home/smith/src/<repo> && git worktree list
ls /home/smith/src/<repo>/.git/worktrees/ 2>/dev/null   # 不该再有 <dir>
```

全部目录清完后：

```bash
rm /home/smith/src/.xbot-worktrees/registry.json   # 唯一读者已随工具删除
rmdir /home/smith/src/.xbot-worktrees             # 必须为空才能删掉
```

## 4. 明令禁止

1. ⛔ `git worktree prune`（任何仓库、任何参数）—— 它会把**别的会话正在用的**
   工作树登记一并清掉（`port-v0521-taizi` 就是这样被误伤的高危目标）。
2. ⛔ 动 `port-v0521-taizi` —— 它仍被另一个会话使用，清理前必须由该会话的负责人确认。
3. ⛔ `rm -rf /home/smith/src/.xbot-worktrees` 整目录删 —— 会连带删掉在用的工作树与
   未提交的改动，且 git 侧留下孤儿登记。
4. ⛔ 把本 runbook 写成定时任务/自愈钩子 —— 这类目录的"还在用吗"只有人（或占用它的
   会话）能判断。

## 5. 验收

```bash
ls -la /home/smith/src/.xbot-worktrees/ 2>&1            # 目录应不存在或只剩在用的那个
cd /home/smith/src/<repo> && git worktree list          # 只剩主工作树 + 在用的工作树
```

清理不属于发布前置：**带着这些目录继续跑是安全的**（代码已不读它们）；本文件存在
的目的只是让遗留物有明确、可追溯的处置路径，而不是被自动脚本悄悄抹掉。
