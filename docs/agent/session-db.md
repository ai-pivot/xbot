# 每会话一个 DB（one session, one DB，v71）

> 2026-09-28 落地。背景：所有会话的消息记录此前共用一个主库（实测 3.4GB 且持续
> 增长），单写者（writeMu 全局串行）+ 删除不回收空间 + 迁移/备份代价随全库线性
> 增长。拆分后：`session_messages` + `iteration_history` 落进每会话独立 SQLite
> 文件，主库只保留注册表（tenants，含 db_path/migrated/preview 列）与全局/用户级
> 数据。

## 架构

```
~/.xbot/
├── xbot.db                      # 主库：tenants 注册表 + 全局/用户级表
└── sessions/<channel>/<bucket>/<name>.db   # 会话库（每会话一个）
```

- **会话库**（`storage/sqlite/sessiondb.go`）：`session_messages` +
  `iteration_history`（tenant_id 列保留、值恒定 —— 全部 `WHERE tenant_id = ?`
  查询代码零改动）。FK 省略（无 tenants 表）。独立 schema 版本（`sessionSchemaVersion`，
  与主库版本链完全独立的命名空间）。每库独立 writeMu + WAL —— **不同会话的写真正
  并行**（拆分的核心收益）。
- **主库**：tenants（+ `db_path`/`migrated`/`preview` 三列，v71）+ 全局/用户级表
  （subscriptions/settings/cron/usage/...）+ 旧 session_messages（迁移源，P4 才清）。

## 并发模型（用户硬性要求：任意数量并发会话）

**无 LRU 上限** —— 活跃会话的库永不关闭。池的生命周期 = TenantSession 缓存
生命周期：

- TenantSession 驱逐（24h 空闲，`cleanupInactiveResources`）→ 关库（checkpoint + close）
- `DestroySession` → 关库 + 删文件（空间立即回收）
- 停机（`Close`）→ `closeAllSessionDBs`（逐库 checkpoint —— gotcha「停机必须
  checkpoint」对每个会话库同样成立）
- 孤儿清扫（`sweepIdleSessionDBs`，5 分钟周期）：无缓存 TenantSession 且 1h 无
  访问的直连打开（`SessionServiceFor` 路径，如 usage 查询）→ 关闭（fd 卫生）

连接池调优（`OpenSessionDB`）：`MaxIdleConns(1)` + `ConnMaxIdleTime(5min)` ——
空闲会话只占 1 个连接，5 分钟无查询后连接归还（fd 预算与活跃会话数解耦）；
活跃会话最多 4 个并发查询（与主库一致）。

## 惰性迁移（migrated=0 → 1）

`sessionDB(tenantID)`（`session/sessiondb.go`）：池未命中 → 解析注册表
（tenants.db_path，空则派生 `SessionDBRelPath` 并持久化）→ 打开会话库 → 若
`migrated=0` → ATTACH 主库 + 单事务 DELETE+INSERT（**显式列名** —— 主库迁移链的
物理列序与 createSchema 不同，`SELECT *` 按列位复制会错位）→ preview 回填 →
置 `migrated=1`（**最后一步** —— 它是「重跑迁移会清空会话库写入」的闸门）。

幂等：迁移中断重跑安全（DELETE 先清空目标，崩溃在 COMMIT 前则事务回滚，崩溃
在 COMMIT 后、置 migrated=1 前则重跑 = 再清空再复制，结果一致）。**migrated=1 后
永不重跑**（否则会清空迁移后写入的新数据）。

## 关键收口点（改代码前必读）

- **消息读写入口**：`TenantSession.sessionSvc`（绑定会话库，GetOrCreateSession
  构造）+ `MultiTenantSession.SessionServiceFor(tenantID)`（直接构造点收口 ——
  rpc contextUsage / usage stats / rewind 等只有 tenantID 的路径）。
  **绝不再 `sqlite.NewSessionService(主库)`**（那会读写主库的旧 session_messages）。
- **preview（跨会话列表）**：主库 `tenants.preview` 列（写入路径维护：
  TenantSession 的 append 钩子 / 迁移回填 / rewind/clear 重算；读取路径：
  ListUserChats / listTenantsByChannel / listTenantsForSender 读这一列 —— 拆库后
  主库没有消息数据，跨库 JOIN 不可能）。eligible = role IN (user, assistant) 且
  非 display_only（追加的 eligible 消息就是最新一条：id 单调递增）。
- **CWD**：`TenantService.SetTenantCWD/GetTenantCWD`（主库 tenants 表 —— 从
  SessionService 迁出，SessionService 现在绑定会话库没有 tenants 表）。
- **RewindToHistoryID**：只操作会话库表（截断）；tenant_state（主库）的 token 水位
  恢复由调用方（TenantSession，持有主库 MemoryService）写 —— 跨库无法原子，水位
  是派生缓存（下一条用户消息的 SaveContextTokens 自愈）。
- **SetTenantSubscription**（模型切换）：tenant_state 清零（主库）留在
  TenantService；session_messages.context_tokens 清零（会话库）经 LLMFactory 的
  `sessionTokenResetter` 钩子（`resetSessionTokenBaseline` → 模型变更时
  `MultiTenantSession.ResetSessionContextTokens`）。
  - ⚠️ **现状（2026-09-30 审查，commit a9d8695c）钩子从不触发**：
    `resetSessionTokenBaseline`（`agent/llm_factory.go:450`）在
    `SetTenantSubscription` **之后**回读 `GetTenantSubscription` 判"是否变更" ——
    读到的必然就是刚写入的新值 ⇒ 恒等 ⇒ 早退，`ResetSessionContextTokens` 永不执行
    （探针实证：`TestGetContextUsageRPC_ExactSnapshotFallback` 走 SelectModel 时
    无任何 ResetSession 调用）。主库半边（tenant_state）仍由
    `SetTenantSubscription` 内部按"写入前读到的旧值"正确清零。修法：变更判定移到
    写入前（或让 SetTenantSubscription 返回 changed 标志）。当前后果仅限「切换模型
    后本 turn 的 usage 基线沿用旧值」，下一条用户消息自愈，无数据丢失。
- **GetTenantUsageStats**：iteration_history 聚合（会话库）+ tenant_state/tenants
  元数据（主库，调用方 `fillUsageStatsFromMainDB` 补齐）。

## 测试模式（gotcha）

- **触发器注入/直写断言必须打会话库**：`mt.DB()` 是主库 —— session_messages/
  iteration_history 的触发器（`CREATE TRIGGER ... ON session_messages`）和行断言
  必须经 `mt.SessionDBFor(sess.TenantID())`（agent 测试 helper：`sessionDBConn(t, mt, sess)`）。
  打在主库上对 appends 不生效（测试静默通过 = 假绿）。
- **迁移对账**：`TestLazyMigrationCopiesRowsAndSetsFlag`（行数/内容一致 + migrated
  标记 + **主库行已删**——v72 契约）+ `TestLazyMigrationIsIdempotentOnReopen`
  （migrated=1 后重开不清空迁移后写入）+ `TestLazyMigrationFlagBeforeDelete`
  （**顺序判别**：置标记失败时主库行必须在 —— 删除先于标记 = 数据丢失排序）。
- **v72 删除迁移**：`TestMigrateV71ToV72BulkCompletesAndDeletes`（三态归宿：
  已迁移/残留/孤儿）+ `_IsIdempotent` + `TestMigrateV72SkipsFailingStraggler`
  （单租户失败不阻塞启动）+ `TestMigrateV72VacuumReclaimsSpace`（体积收缩 +
  freelist 清零）。
- **并发无上限**：`TestConcurrentSessionsBeyond32`（40 个并发会话全部可写 —— 任何
  形式的"最多 N 个打开"都会让它红）。
- **写隔离**：`TestSessionWriteIsolation`（两会话写各自库文件，互不污染）。
- **删除回收**：`TestDestroySessionDeletesFile`（DestroySession 后文件消失）。

## v72：删除迁移（deletion migration，2026-09-30）

v71 上线后惰性迁移把会话数据复制进了独立库，主库的 session_messages/
iteration_history 只剩冗余副本。**v72 把主库缩小回注册表 + 全局表**（3.5G → MB 级）：

```
启动链 v71→v72（migrateV71ToV72，全步骤幂等）：
1. 批量补齐残留：migrated=0 的租户逐个走与惰性路径同一份复制核心
   （DB.CopyTenantDataFromMainDB —— 已下沉 storage/sqlite，杜绝两份 SQL 漂移）
   → 置 migrated=1。单租户失败只 WARN + 跳过（数据留主库、惰性路径接管）——
   启动路径永不阻塞。
2. 删除：migrated=1 租户的主库行 + 孤儿行（tenant_id 不在 tenants，FK 关闭期
   遗留）。DELETE 不 DROP（空表壳无害，残留者惰性路径的 SELECT 仍有效）。
   手工迁移测试 fixture（缺表/缺 tenant_id 列）用 tableExists/columnExists
   守卫跳过（与 v63/v64/v66 同模式）。
3. VACUUM + wal_checkpoint(TRUNCATE)：WAL 模式下 VACUUM 的产物先进 -wal，
   主文件要等 checkpoint 才收缩 —— 必须紧跟 checkpoint 让回收立即可见。
4. version=72（最后一步 —— 崩溃在此之前则下次启动重跑幂等步骤）。
```

**⛔ 顺序契约（防数据丢失核心，三处文档 + 判别测试守护）**：
`migrated=1` 标记必须**先于**主库行删除（惰性路径与迁移链同序）。顺序反了的话，
「删了行但标记未置」+ 崩溃 ⇒ 下次迁移从空主库重拷 = 会话库数据被清空。
标记先删后 = 崩溃只留无害冗余行。判别测试：`TestLazyMigrationFlagBeforeDelete`
（触发器让置标记失败 → 断言主库行还在）。

**惰性路径补删（不变量收口）**：v72 后残留者（批量补齐时失败的）惰性迁移成功时
同步删主库行（`TenantService.DeleteTenantHistory`）→ 不变量「主库 session_messages
只持有 migrated=0 残留者的数据，随时间归零」。

## 已知边界

- v72 后**修复向前**（fix-forward）是唯一回滚路径：回滚旧二进制会看到空历史
  （旧代码读主库 session_messages，已被 v72 删空）。还原点 = 迁移链的自动整库备份
  `xbot.db.pre-v71.bak` + 会话库文件本身（v72 不触碰）。
- `storage/migrate.go`（旧文件迁移工具）仍写主库 session_messages —— 它是 v71 之前
  的遗留路径，新数据全部走会话库；其写入的行会随下一轮迁移被搬走。

## 升级运维注意

- **多进程共享同一 `xbot.db` 时升级只跑一个进程**（全仓无迁移锁）：server 与 CLI
  同时启动新二进制时，两进程可能并发跑迁移链（幂等但 `VACUUM` 可能互挡 ——
  失败只 WARN，下次启动回收）。升级前先停 CLI 进程再重启 server。
- **直升路径（v70 → v72 一次性跨版）**：步骤 1 会同步把**全部**租户的消息拷进
  各自会话库（GB 级时分钟级），`VACUUM` 需 ≈ 库大小的磁盘余量；迁移每完成
  25 个租户打一条进度日志。
- **删除守卫（F2）**：步骤 2 删除前对每个 migrated=1 租户 `os.Stat` 校验会话库
  文件仍在 —— 缺失（外部清理 sessions/）时降级 migrated=0（数据留主库，惰性
  路径下次打开时重拷），绝不静默删除唯一副本。

## ⛔ 新会话永不迁移（2026-09-30 生产事故根治）

**现象**：新建会话报 `open session db for tenant N: migrate session db: attach main
db: disk I/O error (522)`（522 = `SQLITE_IOERR_SHORT_READ`），子代理 spawn 同样失败；
几分钟后自愈；同机新起进程读同一库却正常（存量连接坏的、新 ATTACH 坏的）。

**根因（代码缺陷，确定）**：惰性迁移对**所有** `migrated=0` 租户无条件跑
ATTACH+复制 —— 全新会话在主库零行，ATTACH 是纯风险零收益。事故时主库 WAL 状态
坏死（`-wal` 被截断为 0 字节 + `-shm` 缺失），服务内存量连接持有失效的 WAL 索引
假设 ⇒ 任何**新 ATTACH** 都 522。用户原话精准：「新建会话又不需要迁移」。

**修复**：`SessionService.HasLegacyRows(tenantID)` —— 经主库**现有连接**（服务
一直在用、事故中唯一没坏的路径）探测 session_messages/iteration_history 有无残留
行；两表皆空 ⇒ **完全不 ATTACH**、不回填 preview，仅置 `migrated=1`（新建会话的
绝大多数路径）。有残留行才走复制（复制失败 = 明确报错，绝不静默丢历史）。
判别测试 `session/sessiondb_test.go` 的 `TestNewSessionDoesNotAttachMainDB`：把主库
文件**改名**（新 ATTACH 必失败、存量池连接不受影响 —— 与事故现场同构）后新会话
必须仍能创建；**变异自证**：探针恒 true（恢复旧的无条件迁移）⇒ 该测试必红。

## 主库损坏的容错与修复（2026-09-30）

- **v72 删除段容错**：DELETE 失败只 WARN + 继续（损坏库上 `SQLITE_CORRUPT` 不阻塞
  启动 —— 铁律「启动路径永不阻塞」）；F2 守卫的下降级 UPDATE 失败 ⇒ **整段删除
  跳过**（fail-safe：绝不删一个会话库文件已缺失的租户的主库行）。版本号仍推进到
  72（避免每次启动重跑失败的 GB 级删除）；残留行是不可达的冗余副本，修库后可清。
- **主库页级损坏的修复流程**（本次生产实例 iteration_history 报 4 处 b-tree 结构
  错误：`invalid page number` / `2nd reference to page` / overflow list 长度错乱）：
  ```bash
  # 1) 停服（干净停机：会话库 checkpoint + 主库关闭）
  $SUP stop xbot-server
  # 2) 原样备份三件套（xbot.db / -wal / -shm）到 repair-<ts>/ 再动手
  # 3) .recover 重建（跳过坏行、重建全部可读数据）
  sqlite3 repair/xbot.db.raw ".recover" | sqlite3 repair/xbot.db.repaired
  # 4) 校验：integrity_check == ok + 关键表行数对账（tenants 必须相等；
  #    iteration_history 只允许多行丢失、不允许凭空多）+ schema_version 不变
  # 5) 校验通过才换入：原库改名留证据 + **删除坏 -wal/-shm**（关键：坏 WAL 索引
  #    会让存量连接持续 522）
  ```
  本仓库的修复脚本模板：`/tmp/xbot_db_repair.sh`（停服 → 备份 → recover → 校验 →
  换入或回滚 → 启服 → 写探针，含 set -uo pipefail 与失败自动回滚）。
- **预防**：① 不要用外部 sqlite3/python 连接直接读写**正在运行**的实例库
  （agent 会话里跑生产库读写探针是本次事故的疑似外部干扰源：`-shm` 被删/`-wal`
  被截断）；② 升级只跑一个进程（无迁移锁）；③ 打开面板即改数据的路径
  （ATTACH）必须只读意图 —— 本次已从「所有 migrated=0 租户」收窄到「确有残留行的
  租户」，风险面缩小到几乎为零。
