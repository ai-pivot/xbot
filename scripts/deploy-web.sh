#!/usr/bin/env bash
# deploy-web.sh —— 把 web 前端产物**整体**（原子）部署到服务端静态目录，并**自证一致性**。
#
#   bash scripts/deploy-web.sh              # npm run build + 整体部署 + 校验
#   bash scripts/deploy-web.sh --no-build   # 跳过构建，只部署当前 web/dist
#   WEB_DIR=… XBOT_HOME=… bash scripts/deploy-web.sh
#
# ── 为什么必须有这个脚本（真实事故，2026-10-14）────────────────────────────────
# 事故形态：`~/.xbot/web/dist` 里 **assets 是新的、index.html 却是旧的** —— 有人把新产物
#   增量拷进旧目录，`index.html` 仍引用旧 bundle 名 ⇒ 服务端吐出的页面**根本不含新代码**。
#   症状极具误导性：原生壳发 `?xbot_panel=files` 深链，页面看着"打开了"，但新协议一行都没跑
#   （用户体感 = "文件管理打不开"）。判据只有一条能抓：**线上 index.html 引用的那个 bundle
#   里到底有没有新符号**（本例 `xbot_panel`）。
# ⇒ 本脚本把这条路封死：① 整体替换（旧目录 mv 走，不 merge）② 部署后**必须**过一致性校验
#   （index.html → 引用的 bundle 存在 + 含探针符号），不过则 exit 1。
#
# ⛔ 不做 `rm -rf`：旧目录只做原子 `mv` 让位（可回滚），符合本仓「禁 rm -rf」纪律。
set -euo pipefail

PROBE="${PROBE:-xbot_panel}"                     # 必须存在于新 bundle 里的探针符号
WEB_DIR="${WEB_DIR:-$HOME/src/xbot/web}"
XBOT_HOME="${XBOT_HOME:-$HOME/.xbot}"
DEST="$XBOT_HOME/web/dist"
DO_BUILD=1
[ "${1:-}" = "--no-build" ] && DO_BUILD=0

echo "== 1/5 参数 =="
echo "  WEB_DIR   = $WEB_DIR"
echo "  DEST      = $DEST"
echo "  DO_BUILD  = $DO_BUILD    PROBE = $PROBE"
[ -d "$WEB_DIR" ] || { echo "❌ WEB_DIR 不存在：$WEB_DIR" >&2; exit 1; }

if [ "$DO_BUILD" = "1" ]; then
  echo "== 2/5 构建（npm run build）=="
  ( cd "$WEB_DIR" && npm run build ) || { echo "❌ 构建失败" >&2; exit 1; }
else
  echo "== 2/5 跳过构建 =="
fi
SRC="$WEB_DIR/dist"
[ -f "$SRC/index.html" ] || { echo "❌ 产物缺 index.html：$SRC" >&2; exit 1; }

echo "== 3/5 部署前校验源产物（防把混合态再传一遍）=="
BUNDLE_REL="$(grep -o 'assets/index-[A-Za-z0-9_-]*\.js' "$SRC/index.html" | head -1)"
[ -n "$BUNDLE_REL" ] || { echo "❌ 产物 index.html 里找不到 assets/index-*.js 引用" >&2; exit 1; }
if [ ! -f "$SRC/$BUNDLE_REL" ]; then
  echo "❌ index.html 引用 ${BUNDLE_REL}，但产物里没有这个文件（**混合态产物**）⇒ 拒绝部署" >&2
  exit 1
fi
if ! grep -q "$PROBE" "$SRC/$BUNDLE_REL"; then
  echo "❌ 产物 bundle $BUNDLE_REL 里没有探针符号 '$PROBE' ⇒ 这份产物不含深链支持，拒绝部署" >&2
  exit 1
fi
echo "  ✅ 源产物一致：index.html → ${BUNDLE_REL}（含 ${PROBE}）"

echo "== 4/5 整体替换（旧目录 mv 让位，⛔ 不 merge、不 rm -rf）=="
mkdir -p "$XBOT_HOME/web"
if [ -d "$DEST" ]; then
  BAK="$DEST.bak-$(date +%Y%m%d%H%M%S)"
  mv "$DEST" "$BAK"
  echo "  旧目录 → $BAK"
fi
cp -R "$SRC" "$DEST"

echo "== 5/5 部署后校验（线上 index.html + 引用 bundle）=="
LIVE_REL="$(grep -o 'assets/index-[A-Za-z0-9_-]*\.js' "$DEST/index.html" | head -1)"
[ "$LIVE_REL" = "$BUNDLE_REL" ] || {
  echo "❌ 部署后 index.html 指向 ${LIVE_REL}，期望 $BUNDLE_REL" >&2; exit 1; }
grep -q "$PROBE" "$DEST/$LIVE_REL" || {
  echo "❌ 部署后 bundle 里没有 '$PROBE'" >&2; exit 1; }
echo "  ✅ $DEST/index.html → ${LIVE_REL}（含 ${PROBE}）"

echo "== 完成 =="
echo "  ⚠️ 服务端若在跑：静态目录是**每次请求读盘**（无需重启）；但浏览器/WebView 可能缓存"
echo "     index.html（本服务端已发 Cache-Control: no-cache），assets 是内容哈希名 ⇒ 换名即失效。"
echo "  验证线上真值： curl -s http://localhost:8082/ | grep -o 'assets/index-[A-Za-z0-9_-]*\\.js'"
