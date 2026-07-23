#!/bin/bash
# AgentsView 本地启动（内嵌前端的构建产物）
#
# 等价于官方流程:
#   make build          # 若二进制不存在时
#   ./agentsview serve  # 前台服务，默认 http://127.0.0.1:8080
#
# 双击或在终端执行本脚本均可。按 Ctrl+C 停止服务。
# 更完整的说明见仓库根目录「本地构建说明.md」。

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

BINARY="$PROJECT_DIR/agentsview"
URL="http://127.0.0.1:8080"

notify() {
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display notification \"$1\" with title \"AgentsView\"" >/dev/null 2>&1 || true
  fi
}

die() {
  echo "错误: $1" >&2
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display dialog \"$1\" buttons {\"确定\"} default button 1 with icon stop" >/dev/null 2>&1 || true
  fi
  exit 1
}

command -v make >/dev/null 2>&1 || die "未找到 make，请先安装 Xcode Command Line Tools 或 GNU make"
command -v go >/dev/null 2>&1 || die "未找到 go，需要 Go 1.26+（并启用 CGO）"
command -v node >/dev/null 2>&1 || die "未找到 node，需要 Node.js 22+"

if [ ! -x "$BINARY" ]; then
  echo "未找到可执行文件 agentsview，正在执行 make build ..."
  make build || die "make build 失败，请查看上方日志"
fi

# 若已有本仓库二进制在跑，先让官方 stop 收口（忽略未运行的情况）
if [ -x "$BINARY" ]; then
  "$BINARY" serve stop >/dev/null 2>&1 || true
  "$BINARY" daemon stop >/dev/null 2>&1 || true
fi

echo "启动 AgentsView: $URL"
echo "日志在本终端输出；按 Ctrl+C 停止。"
echo ""

notify "正在启动: $URL"

# serve 默认会尝试打开浏览器；失败也不阻断
exec "$BINARY" serve
