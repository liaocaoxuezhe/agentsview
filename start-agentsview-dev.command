#!/bin/bash
# AgentsView 开发模式启动
#
# 等价于官方流程（两个终端）:
#   make air-install    # 首次需要
#   make dev            # Go 后端 + air 热重载，默认 :8080
#   make frontend-dev   # Vite 前端开发服务器，默认 :5173
#
# 访问 http://127.0.0.1:5173 （Vite 代理 /api 到后端）
# 按 Ctrl+C 同时停止前后端。更完整的说明见「本地构建说明.md」。

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

FRONTEND_DIR="$PROJECT_DIR/frontend"
GO_LOG="/tmp/agentsview-dev-go.log"
VITE_LOG="/tmp/agentsview-dev-vite.log"
VITE_URL="http://127.0.0.1:5173"
API_URL="http://127.0.0.1:8080"

GO_PID=""
VITE_PID=""

notify() {
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display notification \"$1\" with title \"AgentsView Dev\"" >/dev/null 2>&1 || true
  fi
}

die() {
  echo "错误: $1" >&2
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display dialog \"$1\" buttons {\"确定\"} default button 1 with icon stop" >/dev/null 2>&1 || true
  fi
  exit 1
}

cleanup() {
  echo ""
  echo "正在停止开发服务..."
  if [ -n "${VITE_PID}" ] && kill -0 "${VITE_PID}" 2>/dev/null; then
    kill "${VITE_PID}" 2>/dev/null || true
    wait "${VITE_PID}" 2>/dev/null || true
  fi
  if [ -n "${GO_PID}" ] && kill -0 "${GO_PID}" 2>/dev/null; then
    # air 会拉起子进程，尽量整组退出
    kill "${GO_PID}" 2>/dev/null || true
    wait "${GO_PID}" 2>/dev/null || true
  fi
  # 兜底：停掉本仓库 tmp 下由 air 编译的二进制
  if [ -x "$PROJECT_DIR/tmp/agentsview" ]; then
    "$PROJECT_DIR/tmp/agentsview" serve stop >/dev/null 2>&1 || true
  fi
  echo "已停止。日志: $GO_LOG , $VITE_LOG"
}

trap cleanup EXIT INT TERM

command -v make >/dev/null 2>&1 || die "未找到 make"
command -v go >/dev/null 2>&1 || die "未找到 go，需要 Go 1.26+（并启用 CGO）"
command -v node >/dev/null 2>&1 || die "未找到 node，需要 Node.js 22+"
command -v npm >/dev/null 2>&1 || die "未找到 npm"

# air：make dev 依赖它做 Go 热重载
if ! command -v air >/dev/null 2>&1 \
  && [ ! -x "$(go env GOPATH 2>/dev/null)/bin/air" ] \
  && [ ! -x "$(go env GOBIN 2>/dev/null)/air" ]; then
  echo "未找到 air，正在执行 make air-install ..."
  make air-install || die "make air-install 失败"
fi

if [ ! -d "$FRONTEND_DIR/node_modules" ]; then
  echo "前端依赖未安装，正在 frontend && npm ci ..."
  (cd "$FRONTEND_DIR" && npm ci) || die "npm ci 失败"
fi

# 清理可能残留的旧 PID / 旧 serve
if [ -x "$PROJECT_DIR/agentsview" ]; then
  "$PROJECT_DIR/agentsview" serve stop >/dev/null 2>&1 || true
  "$PROJECT_DIR/agentsview" daemon stop >/dev/null 2>&1 || true
fi
rm -f "$PROJECT_DIR/.agentsview-go.pid" "$PROJECT_DIR/.agentsview-vite.pid" "$PROJECT_DIR/.agentsview.pid"

echo "启动 Go 后端 (make dev / air) ..."
# make dev 会先 restore pricing snapshot，首次可能稍慢
nohup make dev >"$GO_LOG" 2>&1 &
GO_PID=$!

echo "等待后端就绪: $API_URL"
backend_ok=0
for _ in $(seq 1 60); do
  if curl -sf "$API_URL/api/v1/health" >/dev/null 2>&1; then
    backend_ok=1
    break
  fi
  if ! kill -0 "$GO_PID" 2>/dev/null; then
    die "make dev 已退出，请查看日志: $GO_LOG"
  fi
  sleep 1
done
if [ "$backend_ok" -ne 1 ]; then
  die "后端 60s 内未就绪，请查看日志: $GO_LOG"
fi
echo "Go 后端已就绪: $API_URL"

echo "启动 Vite 前端 (make frontend-dev) ..."
nohup make frontend-dev >"$VITE_LOG" 2>&1 &
VITE_PID=$!

echo "等待前端就绪: $VITE_URL"
frontend_ok=0
for _ in $(seq 1 60); do
  if curl -sf "$VITE_URL/" >/dev/null 2>&1; then
    frontend_ok=1
    break
  fi
  if ! kill -0 "$VITE_PID" 2>/dev/null; then
    die "make frontend-dev 已退出，请查看日志: $VITE_LOG"
  fi
  sleep 1
done
if [ "$frontend_ok" -ne 1 ]; then
  die "前端 60s 内未就绪，请查看日志: $VITE_LOG"
fi

echo ""
echo "========================================"
echo "AgentsView 开发模式已启动"
echo "  Vite 前端: $VITE_URL  （日常开发请打开这个）"
echo "  Go 后端:   $API_URL"
echo "  后端日志:  $GO_LOG"
echo "  前端日志:  $VITE_LOG"
echo ""
echo "按 Ctrl+C 停止两个服务"
echo "========================================"

notify "开发模式已启动: $VITE_URL"

if command -v open >/dev/null 2>&1; then
  open "$VITE_URL" >/dev/null 2>&1 || true
fi

# 任一子进程退出则结束脚本并触发 cleanup
while kill -0 "$GO_PID" 2>/dev/null && kill -0 "$VITE_PID" 2>/dev/null; do
  sleep 2
done

if ! kill -0 "$GO_PID" 2>/dev/null; then
  echo "后端进程已退出，详见 $GO_LOG" >&2
fi
if ! kill -0 "$VITE_PID" 2>/dev/null; then
  echo "前端进程已退出，详见 $VITE_LOG" >&2
fi
exit 1
