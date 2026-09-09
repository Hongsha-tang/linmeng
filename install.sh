#!/usr/bin/env bash
# ============================================================
# 琳萌 Linmeng · 一键安装脚本（Linux）
# 安装内容：
#   1. 服务二进制   /opt/linmeng/linmeng          （HTTP 监视服务，不进 PATH）
#   2. 运维 CLI     /usr/local/bin/linmeng         （命令名即 linmeng）
#   3. 配置文件     /opt/linmeng/{setting.json,.env}（缺失才生成，绝不覆盖已有）
#   4. systemd 单元 /etc/systemd/system/linmeng.service
# 幂等：可重复执行；已有配置/服务单元保留（除非 --force）。
# 用法：
#   sudo bash install.sh [--no-start] [--force] [server_bin] [cli_bin]
#   默认从当前目录寻找 ./linmeng（服务）与 ./linmeng-cli（CLI）。
#   --no-start  只安装不启动/不自启
#   --force     覆盖已存在的 linmeng.service（默认保留用户改动）
# 卸载：
#   sudo bash install.sh --uninstall          # 停止并移除 CLI 与服务文件
#   sudo bash install.sh --uninstall --purge  # 连同 /opt/linmeng 与缓存一并删除
# ============================================================
set -euo pipefail

APP_DIR="${LINMENG_APP_DIR:-/opt/linmeng}"
CLI_INSTALL="/usr/local/bin/linmeng"
SVC_NAME="linmeng"
UNIT="/etc/systemd/system/${SVC_NAME}.service"
NO_START=0
FORCE=0

usage() {
  cat <<'EOF'
用法：
  sudo bash install.sh [--no-start] [--force] [server_bin] [cli_bin]
    默认从当前目录寻找 ./linmeng（服务）与 ./linmeng-cli（CLI）。
    --no-start  只安装不启动/不自启
    --force     覆盖已存在的 linmeng.service（默认保留用户改动）
  sudo bash install.sh --uninstall          # 停止并移除 CLI 与服务文件
  sudo bash install.sh --uninstall --purge  # 连同 /opt/linmeng 与缓存一并删除
EOF
  exit 0
}

# ---------- 参数解析 ----------
UNINSTALL=0
PURGE=0
POS=()
for arg in "$@"; do
  case "$arg" in
    -h|--help) usage ;;
    --no-start) NO_START=1 ;;
    --force) FORCE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE=1 ;;
    -*) echo "未知参数：$arg（-h 查看用法）" >&2; exit 2 ;;
    *) POS+=("$arg") ;;
  esac
done

SERVER_BIN="${POS[0]:-./linmeng}"
CLI_BIN="${POS[1]:-./linmeng-cli}"

require_root() {
  if [ "$(id -u)" != 0 ]; then
    echo "请以 root 运行：sudo bash $0" >&2
    exit 1
  fi
}

# ---------- 卸载 ----------
do_uninstall() {
  require_root
  echo "== 停止并禁用服务 =="
  systemctl disable --now "${SVC_NAME}" >/dev/null 2>&1 || true
  echo "== 移除运维 CLI =="
  rm -f "$CLI_INSTALL"
  if [ "$PURGE" = 1 ]; then
    echo "== 清除安装目录与 systemd 单元 =="
    systemctl daemon-reload
    rm -f "$UNIT"
    rm -rf "$APP_DIR"
    echo "已卸载（含配置与缓存）。"
  else
    echo "已卸载。保留 $APP_DIR 与 $UNIT（如需彻底清除请加 --purge）。"
  fi
  echo "完成。"
  exit 0
}

# ---------- 安装 ----------
do_install() {
  require_root

  echo "== 校验二进制 =="
  for f in "$SERVER_BIN" "$CLI_BIN"; do
    if [ ! -f "$f" ]; then
      echo "缺少二进制：$f" >&2
      echo "请先构建：go build -o linmeng . && go build -ldflags \"-X main.version=v0.4.1\" -o linmeng-cli ./cmd/linmeng-cli" >&2
      exit 1
    fi
  done
  chmod 0755 "$SERVER_BIN" "$CLI_BIN"

  echo "== 安装服务二进制到 $APP_DIR =="
  mkdir -p "$APP_DIR"
  install -m 0755 "$SERVER_BIN" "$APP_DIR/linmeng"

  echo "== 配置（缺失才生成，已有则保留）=="
  if [ ! -f "$APP_DIR/setting.json" ]; then
    if [ -f ./setting.json ]; then
      install -m 0644 ./setting.json "$APP_DIR/setting.json"
    else
      cat > "$APP_DIR/setting.json" <<'JSON'
{
  "refresh_interval_seconds": 2,
  "history_points": 20,
  "port": 8002,
  "listen_host": "0.0.0.0",
  "auth_enabled": true,
  "session_ttl_minutes": 120,
  "enable_cpu": true,
  "enable_memory": true,
  "enable_disk": true,
  "enable_network": true,
  "enable_host": true,
  "enable_gpu": true,
  "enable_proc": true,
  "enable_fs": true
}
JSON
    fi
    echo "已生成 setting.json"
  else
    echo "保留已有 setting.json"
  fi

  if [ ! -f "$APP_DIR/.env" ]; then
    if [ -n "${AUTH_PASSWORD:-}" ]; then
      printf 'AUTH_PASSWORD=%s\n' "$AUTH_PASSWORD" > "$APP_DIR/.env"
    else
      printf 'AUTH_PASSWORD=admin123\n' > "$APP_DIR/.env"
      echo "已生成 .env（出厂默认密码 admin123，请尽快在页面或 CLI 修改）"
    fi
    chmod 0600 "$APP_DIR/.env"
  else
    echo "保留已有 .env"
  fi

  echo "== 安装运维 CLI → $CLI_INSTALL =="
  install -m 0755 "$CLI_BIN" "$CLI_INSTALL"

  echo "== systemd 单元 =="
  if [ -f "$UNIT" ] && [ "$FORCE" != 1 ]; then
    echo "保留已有 $UNIT（如需覆盖请加 --force）"
  else
    cat > "$UNIT" <<'INI'
[Unit]
Description=Linmeng Linux System Monitor
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/linmeng
ExecStart=/opt/linmeng/linmeng
Restart=on-failure
# 运行用户说明：root 可读取全量 /proc /sys；
# 若改专用低权用户，请确保该用户对 /opt/linmeng 目录可写
# （运行期会写 linmeng-cache/ 并回写 setting.json/.env）。
User=root

[Install]
WantedBy=multi-user.target
INI
    systemctl daemon-reload
    echo "已写入 $UNIT"
  fi

  if [ "$NO_START" = 1 ]; then
    echo "（--no-start：未启动/未自启）"
  elif systemctl is-active --quiet "${SVC_NAME}" 2>/dev/null; then
    echo "== 服务已在运行，执行重启以加载新二进制 =="
    systemctl daemon-reload
    systemctl restart "${SVC_NAME}"
    sleep 1
    systemctl --no-pager status "${SVC_NAME}" | head -n 8 || true
  else
    echo "== 启用并启动服务 =="
    systemctl enable --now "${SVC_NAME}"
    sleep 1
    systemctl --no-pager status "${SVC_NAME}" | head -n 8 || true
  fi

  echo ""
  echo "========== 安装完成 =========="
  echo "服务目录：$APP_DIR"
  echo "运维 CLI：$CLI_INSTALL（版本：$("$CLI_INSTALL" 7 1 2>/dev/null | head -n1 || echo '未知')）"
  echo "验证："
  echo "  systemctl status ${SVC_NAME}"
  echo "  curl -i http://127.0.0.1:8002/login"
  echo "  交互运维：$CLI_INSTALL（进入数字菜单）"
}

case "$UNINSTALL" in
  1) do_uninstall ;;
  0) do_install ;;
esac
