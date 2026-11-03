# Runbook：二进制部署（按实例逐个重启 + 校验）

> 起因：2026-11-02 那次部署把旧二进制留在了一个库已经迁到 schema v80 的实例上。
> 该进程只打一行 `Stored schema version is NEWER than this binary supports`（当时是
> WARN）就继续服务；而 `validateSession` 是纯内存 map（`channel/web/web_auth.go`），
> 进程里没有浏览器持有的那批 token ⇒ 前端全员 401，业务逻辑根本没进，**没有一条
> 业务日志**。两个库是物理隔离的（`/home/smith/.xbot/xbot.db` 与
> `/home/daidai/.xbot/xbot.db`），主实例重启碰不到它 —— 根因只有一个：
> **部署时漏了那一个实例的 restart**，然后潜伏了一天多。

## ⚠️ 执行状态（必读）

**本脚本/本文档未在任何真实生产实例上执行过；仅在 fake supervisorctl / fake curl / 临时日志（`t.TempDir()`）
下由 `scripts/restart_all_instances_test.go` 验证。** 首次真机使用前，请先用 `--instances` 指向一台
非生产实例跑通，再对生产实例执行。

机器验证的 5 个失败条件（每个都有独立用例，用例名可直接 `-run`）：

| # | 失败条件 | 用例（`scripts/restart_all_instances_test.go`） |
|---|---|---|
| 1 | **漏实例**：supervisor 里 RUNNING 但不在实例清单里 | `TestRestartAllInstancesMissedInstanceFails` |
| 2 | **旧二进制**：`/api/health` 的 `binary_sha256` 与新二进制不符 | `TestRestartAllInstancesStaleBinaryFails` |
| 3 | **日志残留**：日志尾部仍有 schema 不匹配告警 | `TestRestartAllInstancesSchemaWarningInLogFails` |
| 4 | **未就绪**：`GET /` 非 200（例：502） | `TestRestartAllInstancesNon200Fails` |
| 5 | **用法错误**：实例清单为空 / 缺 base_url | `TestRestartAllInstancesRefusesEmptyList`（exit 2） |

另有健康端点 `schema_newer=true` 与全绿 happy path 两个用例：
`TestRestartAllInstancesSchemaNewerHealthFails` / `TestRestartAllInstancesHappyPath`。

## 铁律

1. **二进制与会话库 schema 必须同版本交付。** 换二进制的那一次，必须把该实例
   重启并校验，而不是等下次重启「顺带」生效。
2. **按实例逐个确认。** 「它没重启好像也没事」是上一次事故的错误假设。
3. **`GET /` == 200 不足以判定部署成功。** 没重启的旧进程照样回 200；必须读
   `/api/health` 的 `binary_sha256` / `schema_newer` / `binary_changed`。

## 步骤

```bash
# 0) 构建/替换二进制（略）——注意先落盘再重启，避免重启读到半个文件
#    推荐：写临时文件 → mv（原子替换），再逐实例 restart

# 1) 准备实例清单（一行一个实例：program|base_url|log_file）
#    默认路径 $XBOT_HOME/deploy/instances.list
cat > /home/smith/.xbot/deploy/instances.list <<'EOF'
# program|base_url|log_file
xbot-prod|http://127.0.0.1:8080|/home/smith/.xbot/logs/xbot-server.log
xbot-daidai|http://127.0.0.1:8081|/home/daidai/.xbot/logs/xbot-server.log
EOF

# 2) 逐实例重启 + 校验（漏一个实例就 exit 1）
scripts/restart-all-instances.sh \
    --instances /home/smith/.xbot/deploy/instances.list \
    --binary /home/smith/.xbot/bin/xbot-cli
```

脚本会：

| 检查 | 判据 | 失败后果 |
|---|---|---|
| 实例清单完整性 | `supervisorctl status` 里 `RUNNING` 的 `xbot-*` 程序必须都在清单里 | 打印 `MISSED <program>`，exit 1 |
| 重启 | `supervisorctl restart <program>` 返回 0 | 打印该实例失败 |
| 就绪 | `GET <base_url>/` == 200 | 打印状态码 |
| 换了新二进制 | `/api/health` 的 `binary_sha256` == 新二进制 sha256 前 16 位 | 打印「still running the OLD binary」 |
| schema 一致 | `/api/health` 的 `schema_newer` == false | 打印 `schema_binary` / `schema_db` |
| 运行期漂移 | `/api/health` 的 `binary_changed` == false | 打印该实例失败 |
| 日志 | 日志尾部 200 行内无 schema 不匹配告警 | 打印命中的日志行 |

**任一实例失败 ⇒ 脚本 exit 1，并逐行打印是谁。** 不允许「其余都好了就算完成」。

## 事后自查

```bash
# 任何实例都能读到自己的指纹与 schema 版本对（免登录，401 风暴里也读得到）
curl -s http://127.0.0.1:8080/api/health | jq .
# {"status":"ok","binary_sha256":"3f9a...","binary_mtime":"2026-11-03T02:51:00Z",
#  "binary_changed":false,"schema_binary":80,"schema_db":80,"schema_newer":false,
#  "checked_at":"2026-11-03T13:30:00Z"}
```

- `status=degraded` ⇔ `binary_changed || schema_newer`。
- 运行期每 60s（`XBOT_BINARY_DRIFT_INTERVAL` 可覆盖）比对磁盘上的二进制：
  被替换而进程没重启 ⇒ `binary_changed=true` + ERROR 日志。
- `/api/health` **故意不鉴权**：它要报告的正是「全员 401」的场景。载荷只有本进程
  的指纹与版本号，没有用户数据，也没有文件系统路径。

## 回滚

二进制回滚同样走本流程：把旧二进制放回 → `scripts/restart-all-instances.sh`
逐实例重启校验。**不要**只回滚主实例：库的 schema 已经是新的，旧二进制配新库
就是本次事故的形态（`schema_newer=true` 会一直亮着）。

## 相关证据与实现

- `internal/runtimeinfo`：启动快照（sha256+mtime）、漂移检测、schema 版本对。
- `channel/web/web_health.go`：`GET /api/health`。
- `storage/sqlite/db.go`：库比二进制新 ⇒ **ERROR** + 登记到健康端点。
- `scripts/restart-all-instances.sh` + `scripts/restart_all_instances_test.go`。
