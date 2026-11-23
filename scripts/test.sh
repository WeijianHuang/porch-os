#!/usr/bin/env bash
# Porch OS 本地测试脚本（全 Docker，无需在宿主机安装 Go）。
# 所有 go 命令都在 golang:1.25 容器里执行，源码从宿主机挂载进容器。
#
# 前提：
#   1. 宿主机已安装 Docker Desktop / Docker Engine，且当前用户可用 docker 命令
#   2. 项目根目录已有 .env（可从 .env.example 复制并填写）
#   3. 需要数据库的操作（migrate / integration / all）需先执行 db-up
#
# 用法：
#   bash scripts/test.sh <command>
#
# 命令：
#   unit          跑领域/平台层单测（不需要数据库）
#   migrate       在数据库容器里执行 goose 迁移
#   integration   跑集成测试（需要数据库；经 compose 网络连 db:5432）
#   all           migrate + 全量 go test（单测 + 集成测试）
#   db-up         启动本地 PostgreSQL 容器并等待 healthy
#   db-down       停止本地 PostgreSQL 容器（保留数据卷）
#   shell         进入一个挂载了源码的 golang 容器（交互式排查）
#   help          显示本帮助
#
# 说明：
#   - 单测与集成测试共用同一套用例；差异仅在连接串来源：
#     本脚本走「compose 组网」模式（ADR-0002 模式 B），由 TEST_DATABASE_URL 提供。
#   - 若你本机装了 Go 且 Docker daemon 是 Unix socket，可直接用 testcontainers 模式
#     （ADR-0002 模式 A），无需本脚本，直接 go test ./... 即可。

set -euo pipefail

GO_IMAGE="${GO_IMAGE:-golang:1.25}"

# 定位项目根目录（兼容 Windows Git Bash 的盘符路径）
resolve_root() {
  local d
  d="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      MSYS_NO_PATHCONV=1
      (cd "$d" && pwd -W 2>/dev/null) || echo "$d"
      ;;
    *)
      echo "$d"
      ;;
  esac
}
PROJECT_ROOT="$(resolve_root)"
export MSYS_NO_PATHCONV="${MSYS_NO_PATHCONV:-1}"

COMPOSE_NETWORK="${COMPOSE_NETWORK:-${COMPOSE_PROJECT_NAME:-porch-os}_default}"
DB_CONTAINER="${DB_CONTAINER:-porch-os-db}"

log() { printf '\n==> %s\n' "$*"; }
die() { printf '\n错误: %s\n' "$*" >&2; exit 1; }

load_env() {
  [[ -f "$PROJECT_ROOT/.env" ]] || die "缺少 .env，请先复制 .env.example 并填写：cp .env.example .env"
  set -a
  # shellcheck disable=SC1091
  source "$PROJECT_ROOT/.env"
  set +a
}

# 容器内使用的连接串：服务名 db 而非 localhost（容器间经 compose 网络互通）
container_db_url() {
  printf 'postgresql://%s:%s@db:5432/%s?sslmode=disable' \
    "${POSTGRES_USER:-porch}" "${POSTGRES_PASSWORD:-}" "${POSTGRES_DB:-porch}"
}

run_in_go_container() {
  local desc="$1"; shift
  log "$desc"
  docker run --rm \
    -v "$PROJECT_ROOT:/app" \
    -w /app \
    "$@" \
    "$GO_IMAGE" "${GO_CMD[@]}"
}

db_up() {
  log "启动 PostgreSQL 容器"
  (cd "$PROJECT_ROOT" && docker compose up -d db)
  log "等待数据库 healthy"
  for i in $(seq 1 30); do
    local st
    st="$(docker inspect --format='{{.State.Health.Status}}' "$DB_CONTAINER" 2>/dev/null || true)"
    if [[ "$st" == "healthy" ]]; then
      echo "数据库已就绪 ($DB_CONTAINER)"
      return 0
    fi
    sleep 2
  done
  die "数据库在 60 秒内未就绪，请检查 docker compose logs db"
}

db_down() {
  log "停止 PostgreSQL 容器（数据卷保留）"
  (cd "$PROJECT_ROOT" && docker compose stop db)
}

cmd_unit() {
  GO_CMD=(go test ./... -race -count=1)
  run_in_go_container "运行单测（容器内，无需数据库）"
}

cmd_migrate() {
  load_env
  local db_url
  db_url="$(container_db_url)"
  GO_CMD=(go run ./cmd/job migrate)
  run_in_go_container "执行数据库迁移" \
    --network "$COMPOSE_NETWORK" \
    -e DATABASE_URL="$db_url" \
    -e JWT_SECRET="${JWT_SECRET:-}" \
    -e EXPECTED_SCHEMA_VERSION="${EXPECTED_SCHEMA_VERSION:-11}"
}

cmd_integration() {
  load_env
  local db_url
  db_url="$(container_db_url)"
  GO_CMD=(go test ./... -race -count=1)
  run_in_go_container "运行集成测试（compose 组网，连 db:5432）" \
    --network "$COMPOSE_NETWORK" \
    -e DATABASE_URL="$db_url" \
    -e TEST_DATABASE_URL="$db_url" \
    -e JWT_SECRET="${JWT_SECRET:-}"
}

cmd_all() {
  cmd_migrate
  cmd_integration
}

cmd_shell() {
  load_env
  local db_url
  db_url="$(container_db_url)"
  log "进入 golang 容器（源码挂载在 /app，退出用 exit）"
  docker run --rm -it \
    -v "$PROJECT_ROOT:/app" \
    -w /app \
    --network "$COMPOSE_NETWORK" \
    -e DATABASE_URL="$db_url" \
    -e TEST_DATABASE_URL="$db_url" \
    -e JWT_SECRET="${JWT_SECRET:-}" \
    "$GO_IMAGE" bash
}

usage() {
  sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

main() {
  local cmd="${1:-help}"
  case "$cmd" in
    unit)        cmd_unit ;;
    migrate)     cmd_migrate ;;
    integration) cmd_integration ;;
    all)         cmd_all ;;
    db-up)       db_up ;;
    db-down)     db_down ;;
    shell)       cmd_shell ;;
    help|-h|--help) usage ;;
    *) die "未知命令: $cmd（用 help 查看用法）" ;;
  esac
}

main "$@"
