#!/usr/bin/env bash
# linode-panel 一键安装脚本（Linux + systemd）
#
# 安装面板：
#   curl -fsSL https://raw.githubusercontent.com/Fearless743/linode-panel/main/install.sh | sudo bash
#
# 安装远程 Agent：
#   curl -fsSL https://raw.githubusercontent.com/Fearless743/linode-panel/main/install.sh \
#     | sudo bash -s -- --agent --panel https://panel.example.com --code <注册码>
#
# 可选参数：
#   --agent              安装 Agent（默认为面板）
#   --panel <url>        Agent 连接的面板 base URL（--agent 时必填）
#   --code <code>        Agent 首次注册码（注册成功后 token 落盘，可省略）
#   --addr <addr>        面板监听地址（默认 127.0.0.1:8787）
#   --version <tag>      指定版本，如 v1.0.0（默认取最新 Release）
#   -h, --help           显示帮助
set -euo pipefail

OWNER="Fearless743"
REPO="linode-panel"
RAW_BASE="https://github.com/${OWNER}/${REPO}"
API_LATEST="https://api.github.com/repos/${OWNER}/${REPO}/releases/latest"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

MODE="panel"
PANEL_URL=""
REG_CODE=""
ADDR="${PANEL_ADDR:-127.0.0.1:8787}"

usage() {
  cat <<'EOF'
用法：
  install.sh                       安装面板（默认）
  install.sh --agent --panel <url> [--code <注册码>]   安装远程 Agent

参数：
  --agent              安装 Agent（默认为面板）
  --panel <url>        Agent 连接的面板 base URL（--agent 时必填）
  --code <code>        Agent 首次注册码（注册成功后 token 落盘，可省略）
  --addr <addr>        面板监听地址（默认 127.0.0.1:8787）
  --version <tag>      指定版本，如 v1.0.0（默认取最新 Release）
  -h, --help           显示帮助
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --agent)
      MODE="agent"
      ;;
    --panel)
      [ $# -ge 2 ] || { echo "缺少 --panel 的值" >&2; exit 1; }
      PANEL_URL="$2"
      shift
      ;;
    --code)
      [ $# -ge 2 ] || { echo "缺少 --code 的值" >&2; exit 1; }
      REG_CODE="$2"
      shift
      ;;
    --addr)
      [ $# -ge 2 ] || { echo "缺少 --addr 的值" >&2; exit 1; }
      ADDR="$2"
      shift
      ;;
    --version)
      [ $# -ge 2 ] || { echo "缺少 --version 的值" >&2; exit 1; }
      VERSION="$2"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "未知参数：$1" >&2
      usage >&2
      exit 1
      ;;
  esac
  shift
done

if [ "$(id -u)" -ne 0 ]; then
  echo "请以 root 运行（curl ... | sudo bash）" >&2
  exit 1
fi

if [ "$(uname -s)" != "Linux" ]; then
  echo "该脚本仅支持 Linux + systemd；其他系统请直接下载 Release 二进制运行" >&2
  exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "未找到 systemd（systemctl），请手动下载 Release 二进制运行" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64 | amd64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *)
    echo "不支持的架构：$(uname -m)" >&2
    exit 1
    ;;
esac

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$1"
  else
    echo "需要 curl 或 wget" >&2
    exit 1
  fi
}

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  else
    wget -qO "$2" "$1"
  fi
}

# ---------- 解析版本 ----------
if [ -z "$VERSION" ]; then
  VERSION="$(fetch "$API_LATEST" 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1 || true)"
  if [ -z "$VERSION" ]; then
    echo "无法获取最新 Release（仓库可能还没有发布版本）。请用 --version v1.0.0 指定，或先推送 v* 标签触发构建。" >&2
    exit 1
  fi
else
  case "$VERSION" in
    v*) ;;
    *) VERSION="v${VERSION}" ;;
  esac
fi
VER="${VERSION#v}"

ASSET="${REPO}_${VER}_linux_${ARCH}.tar.gz"
URL="${RAW_BASE}/releases/download/${VERSION}/${ASSET}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "下载 ${ASSET} ..."
if ! download "$URL" "${TMP}/${ASSET}"; then
  echo "下载失败：${URL}" >&2
  echo "请确认该版本已发布且包含 linux_${ARCH} 产物（需要推送 tag 触发 release 工作流）。" >&2
  exit 1
fi
tar -xzf "${TMP}/${ASSET}" -C "$TMP"

install -d -m 0755 "$BIN_DIR"

if [ "$MODE" = "agent" ]; then
  if [ -z "$PANEL_URL" ]; then
    echo "安装 Agent 需要 --panel <面板地址>" >&2
    exit 1
  fi

  SERVICE="linode-panel-agent"
  SERVICE_USER="linode-panel-agent"
  DATA_DIR="/var/lib/${SERVICE}"
  CONF_DIR="/etc/${REPO}"
  CONF_FILE="${CONF_DIR}/agent.conf"
  BIN="${BIN_DIR}/linode-panel-agent"

  install -m 0755 "${TMP}/linode-panel-agent" "$BIN"

  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --home-dir "$DATA_DIR" --create-home \
      --shell "$(command -v nologin || echo /bin/false)" "$SERVICE_USER"
  fi
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
  install -d -m 0755 "$CONF_DIR"

  umask 077
  cat >"$CONF_FILE" <<EOF
PANEL_URL=${PANEL_URL}
REG_CODE=${REG_CODE}
EOF
  chmod 600 "$CONF_FILE"

  cat >"/etc/systemd/system/${SERVICE}.service" <<EOF
[Unit]
Description=Linode Panel Agent
Documentation=${RAW_BASE}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
EnvironmentFile=${CONF_FILE}
ExecStart=${BIN} --panel \${PANEL_URL} --code \${REG_CODE} --state ${DATA_DIR}/agent-state.json
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null
  systemctl restart "$SERVICE"

  cat <<EOF

已安装 linode-panel-agent ${VERSION}
  二进制：${BIN}
  配置：  ${CONF_FILE}（PANEL_URL / REG_CODE）
  数据：  ${DATA_DIR}/agent-state.json
  服务：  systemctl status ${SERVICE}
  日志：  journalctl -u ${SERVICE} -f

注册码只在首次注册时使用，token 已保存到状态文件后可以清空配置里的 REG_CODE。
EOF
else
  SERVICE="linode-panel"
  SERVICE_USER="linode-panel"
  DATA_DIR="/var/lib/${SERVICE}"
  CONF_DIR="/etc/${REPO}"
  CONF_FILE="${CONF_DIR}/panel.conf"
  BIN="${BIN_DIR}/linode-panel"

  install -m 0755 "${TMP}/linode-panel" "$BIN"

  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --home-dir "$DATA_DIR" --create-home \
      --shell "$(command -v nologin || echo /bin/false)" "$SERVICE_USER"
  fi
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
  install -d -m 0755 "$CONF_DIR"

  if [ ! -f "$CONF_FILE" ]; then
    cat >"$CONF_FILE" <<EOF
# linode-panel 环境变量（systemd EnvironmentFile）
LINODE_API_BASE=${LINODE_API_BASE:-https://api.linode.com/v4}
# 被墙判定使用的国内探测接口
# AUTO_REBUILD_CHECK_API=${AUTO_REBUILD_CHECK_API:-https://api.24kplus.com/ipcheck}
# 敏感字段（PAT/root 密码/注册码）加密主密钥。留空则在数据目录自动生成 state.db.key。
# LINODE_PANEL_SECRET_KEY=请替换为足够长的随机字符串
EOF
  fi

  cat >"/etc/systemd/system/${SERVICE}.service" <<EOF
[Unit]
Description=Linode Panel
Documentation=${RAW_BASE}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
EnvironmentFile=-${CONF_FILE}
ExecStart=${BIN} --addr ${ADDR} --state ${DATA_DIR}/state.db
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null
  systemctl restart "$SERVICE"

  cat <<EOF

已安装 linode-panel ${VERSION}
  二进制：${BIN}
  配置：  ${CONF_FILE}
  数据：  ${DATA_DIR}/state.db（及密钥文件 state.db.key，二者需一起备份）
  监听：  ${ADDR}
  服务：  systemctl status ${SERVICE}
  日志：  journalctl -u ${SERVICE} -f

初始管理员密码仅在首次初始化数据库时打印，查看：
  journalctl -u ${SERVICE} --no-pager | grep '初始管理员'

默认只监听 127.0.0.1，需要外网访问请用 Nginx/Caddy 反向代理并配置 TLS。
EOF
fi
