#!/usr/bin/env bash
#
# restart-all-instances.sh — 部署收尾：**按实例逐个**重启并校验（2026-11-03 P0-1）
#
# 事故背景（可复算）：11-02 那次部署把二进制换成了不含 v80 的旧版，而 daidai
# 那个实例自己的库已在 11-02 13:11:18 被含 v80 的二进制迁到 80。它的进程从此只打
# 一行 WARN 就继续服务，前端全员 401（validateSession 是纯内存 map，进程里没有
# 那批 token ⇒ 业务逻辑没进 ⇒ 没有业务日志）。两个库是物理隔离的
# （/home/smith/.xbot/xbot.db vs /home/daidai/.xbot/xbot.db），主实例重启碰不到它
# —— 根因一句话：**部署时漏了这一个实例的 restart**。
#
# 所以本脚本的立场是：重启没有「顺带」这回事，一个实例一个实例地确认。
#   · 每个实例 restart 后必须 GET / == 200；
#   · /api/health 的 schema_newer 必须是 false、binary_changed 必须是 false；
#   · 该实例日志尾部不许出现 schema 不匹配告警；
#   · 若给了 --binary，则 /api/health 的 binary_sha256 必须等于新二进制的
#     sha256 前 16 位 —— **这是唯一能证明「它真的换成了新二进制」的信号**：
#     没重启的旧进程照样回 200，日志也可能还是干净的。
#   · supervisor 里正在跑、但不在实例清单里的 xbot-* 程序 ⇒ 直接判「漏了一个」。
#
# 任何一项不过 ⇒ exit 1，并逐个打印失败 / 漏掉的实例名。
#
# ⚠️ 本脚本**未在任何真实生产实例上执行过**：它只在 fake supervisorctl /
#    fake curl / 临时日志（t.TempDir）下由 scripts/restart_all_instances_test.go
#    验证过。首次真机使用前请先 --instances 指向一台非生产实例跑通。
#    机器验证的 5 个失败条件（均有用例，见该测试文件）：
#      1. 漏实例   —— supervisor 里 RUNNING 但不在清单里       TestRestartAllInstancesMissedInstanceFails
#      2. 旧二进制 —— /api/health 的 binary_sha256 与新二进制不符 TestRestartAllInstancesStaleBinaryFails
#      3. 日志残留 —— 日志尾部仍有 schema 不匹配告警            TestRestartAllInstancesSchemaWarningInLogFails
#      4. 未就绪   —— GET / 非 200                            TestRestartAllInstancesNon200Fails
#      5. 用法错误 —— 实例清单为空/缺 base_url                 TestRestartAllInstancesRefusesEmptyList
#    另有健康端点 schema_newer=true 与 happy path 两个用例：
#      TestRestartAllInstancesSchemaNewerHealthFails / TestRestartAllInstancesHappyPath
#
# 用法:
#   scripts/restart-all-instances.sh [--instances FILE] [--binary PATH]
#
# 实例清单格式（默认 $XBOT_HOME/deploy/instances.list，一行一个实例）:
#   program|base_url|log_file
#   xbot-prod|http://127.0.0.1:8080|/home/smith/.xbot/logs/xbot-server.log
#   xbot-daidai|http://127.0.0.1:8081|/home/daidai/.xbot/logs/xbot-server.log
#
# 可注入（测试用，也可用于非标准安装）:
#   SUPERVISORCTL_BIN   (默认 supervisorctl)
#   CURL_BIN            (默认 curl)
#   XBOT_PROGRAM_PREFIX (默认 xbot)   supervisor 里的程序名前缀
#   XBOT_READY_TIMEOUT  (默认 30 秒)  单实例启动等待上限
#   XBOT_READY_INTERVAL (默认 1 秒)   轮询间隔
#   XBOT_LOG_TAIL_LINES (默认 200)    日志检查的尾部行数
#
# exit: 0 全部通过 / 1 有实例未通过验证 / 2 用法或配置错误
# end-of-header

set -uo pipefail

usage() {
    # 头部以哨兵行结束，避免增删注释后行号漂移把代码打出来
    sed -n '2,/^# end-of-header$/p' "$0" | sed 's/^#\{1,2\} \{0,1\}//'
}

INSTANCES_FILE="${XBOT_INSTANCES_FILE:-${XBOT_HOME:-$HOME/.xbot}/deploy/instances.list}"
BINARY=""

while [ $# -gt 0 ]; do
    case "$1" in
        --instances)
            [ $# -ge 2 ] || { echo "error: --instances needs a value" >&2; exit 2; }
            INSTANCES_FILE="$2"; shift 2 ;;
        --instances=*) INSTANCES_FILE="${1#*=}"; shift ;;
        --binary)
            [ $# -ge 2 ] || { echo "error: --binary needs a value" >&2; exit 2; }
            BINARY="$2"; shift 2 ;;
        --binary=*) BINARY="${1#*=}"; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "error: unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

SUPERVISORCTL="${SUPERVISORCTL_BIN:-supervisorctl}"
CURL="${CURL_BIN:-curl}"
PREFIX="${XBOT_PROGRAM_PREFIX:-xbot}"
READY_TIMEOUT="${XBOT_READY_TIMEOUT:-30}"
READY_INTERVAL="${XBOT_READY_INTERVAL:-1}"
LOG_TAIL="${XBOT_LOG_TAIL_LINES:-200}"

trim() {
    local s="${1-}"
    s="${s#"${s%%[![:space:]]*}"}"
    s="${s%"${s##*[![:space:]]}"}"
    printf '%s' "$s"
}

if [ ! -f "$INSTANCES_FILE" ]; then
    echo "error: instance list not found: $INSTANCES_FILE" >&2
    echo "       (create it: one 'program|base_url|log_file' per instance)" >&2
    exit 2
fi

progs=()
urls=()
logfiles=()
while IFS='|' read -r _prog _url _log _rest; do
    _prog="$(trim "${_prog-}")"
    _url="$(trim "${_url-}")"
    _log="$(trim "${_log-}")"
    [ -z "$_prog" ] && continue
    case "$_prog" in \#*) continue ;; esac
    if [ -z "$_url" ]; then
        echo "error: instance '$_prog' has no base_url in $INSTANCES_FILE" >&2
        exit 2
    fi
    progs+=("$_prog")
    urls+=("$_url")
    logfiles+=("$_log")
done < "$INSTANCES_FILE"

count=${#progs[@]}
if [ "$count" -eq 0 ]; then
    echo "error: no instances in $INSTANCES_FILE — refusing to declare the deployment done" >&2
    exit 2
fi

EXPECTED_SHA=""
if [ -n "$BINARY" ]; then
    if [ ! -f "$BINARY" ]; then
        echo "error: --binary path does not exist: $BINARY" >&2
        exit 2
    fi
    EXPECTED_SHA="$(sha256sum "$BINARY" | cut -c1-16)"
    echo "deploying binary: $BINARY (sha256[:16]=$EXPECTED_SHA)"
fi

# ── supervisor 交叉核对：在跑但不在清单里的程序 = 漏掉的实例 ──────────────────
in_list() {
    local want="$1" i
    for ((i = 0; i < count; i++)); do
        [ "${progs[$i]}" = "$want" ] && return 0
    done
    return 1
}

missed_count=0
supervisor_ok=0
if status_out="$("$SUPERVISORCTL" status 2>/dev/null)"; then
    supervisor_ok=1
    while IFS= read -r line; do
        [ -z "$line" ] && continue
        name="$(printf '%s' "$line" | awk '{print $1}')"
        state="$(printf '%s' "$line" | awk '{print $2}')"
        [ -z "$name" ] && continue
        case "$name" in "$PREFIX"*) ;; *) continue ;; esac
        # 只有「正在跑」才说明它本该被本次部署重启过；STOPPED/FATAL 等状态
        # 不会被 restart 检查到，也谈不上「漏重启」。
        [ "$state" = "RUNNING" ] || continue
        if ! in_list "$name"; then
            missed_count=$((missed_count + 1))
            echo "MISSED  $name  (running in supervisor but absent from $INSTANCES_FILE — it was never restarted)"
        fi
    done <<< "$status_out"
else
    echo "warning: '$SUPERVISORCTL status' failed — cannot cross-check for instances missing from $INSTANCES_FILE" >&2
fi

# ── 逐实例：restart → 就绪 → 健康 → 日志 ─────────────────────────────────────
json_field() {
    # json_field <json> <key> — 不依赖 jq（部署机不一定装了）
    local json="$1" key="$2"
    printf '%s' "$json" \
        | grep -o "\"$key\"[[:space:]]*:[[:space:]]*\(true\|false\|null\|-\{0,1\}[0-9][0-9.eE+-]*\|\"[^\"]*\"\)" \
        | head -n 1 | sed 's/^[^:]*:[[:space:]]*//' | sed 's/^"//; s/"$//'
}

failed=0
failed_names=""

for ((i = 0; i < count; i++)); do
    prog="${progs[$i]}"
    url="${urls[$i]}"
    logfile="${logfiles[$i]}"
    reasons=""

    if ! restart_out="$("$SUPERVISORCTL" restart "$prog" 2>&1)"; then
        reasons="${reasons}        - supervisorctl restart failed: $(printf '%s' "$restart_out" | tr '\n' ' ')
"
    fi

    # 就绪：GET / 必须是 200（部署成功的旧定义，保留）
    code="000"
    waited=0
    while :; do
        code="$("$CURL" -s -o /dev/null -w '%{http_code}' --max-time 5 "$url/" 2>/dev/null || true)"
        [ "$code" = "200" ] && break
        [ "$waited" -ge "$READY_TIMEOUT" ] && break
        sleep "$READY_INTERVAL"
        waited=$((waited + READY_INTERVAL))
    done
    if [ "$code" != "200" ]; then
        reasons="${reasons}        - GET / returned $code after ${waited}s (want 200)
"
    fi

    # 健康端点：schema_newer=false、binary_changed=false；给了 binary 还要指纹一致
    health="$("$CURL" -s --max-time 5 "$url/api/health" 2>/dev/null || true)"
    if [ -z "$health" ]; then
        reasons="${reasons}        - GET /api/health returned nothing
"
    else
        schema_newer="$(json_field "$health" schema_newer)"
        if [ "$schema_newer" = "true" ]; then
            reasons="${reasons}        - health reports schema_newer=true (schema_binary=$(json_field "$health" schema_binary) schema_db=$(json_field "$health" schema_db))
"
        elif [ -z "$schema_newer" ]; then
            reasons="${reasons}        - health payload has no schema_newer field: $(printf '%s' "$health" | tr '\n' ' ' | cut -c1-200)
"
        fi
        if [ "$(json_field "$health" binary_changed)" = "true" ]; then
            reasons="${reasons}        - health reports binary_changed=true (binary on disk was replaced but this process is stale)
"
        fi
        if [ -n "$EXPECTED_SHA" ]; then
            got_sha="$(json_field "$health" binary_sha256)"
            if [ "$got_sha" != "$EXPECTED_SHA" ]; then
                reasons="${reasons}        - health binary_sha256=${got_sha:-<empty>} but the deployed binary is $EXPECTED_SHA — this instance is still running the OLD binary
"
            fi
        fi
    fi

    # 日志：尾部不许有 schema 不匹配告警
    if [ -z "$logfile" ]; then
        reasons="${reasons}        - no log_file in the instance list — cannot verify the schema warning
"
    elif [ ! -f "$logfile" ]; then
        reasons="${reasons}        - log file not found: $logfile
"
    else
        hit="$(tail -n "$LOG_TAIL" "$logfile" 2>/dev/null | grep -E 'NEWER than this binary supports|Stored schema version is NEWER' | tail -n 1)"
        if [ -n "$hit" ]; then
            reasons="${reasons}        - log tail still has a schema mismatch warning: $(printf '%s' "$hit" | cut -c1-200)
"
        fi
    fi

    if [ -z "$reasons" ]; then
        echo "OK    $prog  $url"
    else
        failed=$((failed + 1))
        failed_names="$failed_names $prog"
        echo "FAIL  $prog  $url"
        printf '%s' "$reasons"
    fi
done

echo
if [ "$failed" -ne 0 ] || [ "$missed_count" -ne 0 ]; then
    echo "DEPLOY FAILED: $failed of $count instance(s) failed verification, $missed_count instance(s) missed."
    [ "$failed" -ne 0 ] && echo "  failed:$failed_names"
    [ "$supervisor_ok" -eq 0 ] && echo "  NOTE: supervisor status was unreadable; the MISSED list may be incomplete."
    exit 1
fi

echo "DEPLOY OK: all $count instance(s) restarted and verified."
exit 0
