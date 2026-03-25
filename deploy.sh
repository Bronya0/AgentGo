#!/usr/bin/env bash
# deploy.sh — 首次部署 mini-agent
#   1. 创建专用低权限用户 mini-agent（nologin, 家目录 = 安装目录）
#   2. 准备运行目录、.agent 数据子目录和配置
#   3. 安装 systemd service（安全加固）
#
# 用法（root）:
#   bash deploy.sh <二进制路径> <安装根目录> [config.yaml 路径]
#
# 示例:
#   bash deploy.sh ./dist/agent-linux-amd64 /data/agent
#   bash deploy.sh ./dist/agent-linux-amd64 /opt/mini-agent ./config.yaml

set -euo pipefail

# ---------- 参数 ----------
BINARY="${1:?用法: deploy.sh <binary> <安装根目录> [config.yaml]}"
APP_DIR="${2:?用法: deploy.sh <binary> <安装根目录> [config.yaml]}"
CONFIG="${3:-}"

APP_USER="mini-agent"
BIN_PATH="${APP_DIR}/agent"
CONF_PATH="${APP_DIR}/config.yaml"
AGENT_DATA="${APP_DIR}/.agent"          # session + memory 写到这里
SERVICE_NAME="mini-agent"

# ---------- 必须 root ----------
if [ "$(id -u)" -ne 0 ]; then
    echo "错误: 请使用 root 或 sudo 执行此脚本" >&2
    exit 1
fi

# ---------- 创建低权限用户（家目录 = 安装目录）----------
if ! id "$APP_USER" &>/dev/null; then
    useradd --system --shell /usr/sbin/nologin --home-dir "$APP_DIR" --no-create-home "$APP_USER"
    echo "已创建系统用户: $APP_USER (home=$APP_DIR)"
else
    echo "用户 $APP_USER 已存在，跳过"
fi

# ---------- 创建目录 ----------
# 安装根目录：仅 owner rwx, group rx
install -d -m 0750 -o "$APP_USER" -g "$APP_USER" "$APP_DIR"
# .agent 数据目录（sessions / memory）：仅 owner rwx
install -d -m 0700 -o "$APP_USER" -g "$APP_USER" "$AGENT_DATA"
install -d -m 0700 -o "$APP_USER" -g "$APP_USER" "${AGENT_DATA}/sessions"
install -d -m 0700 -o "$APP_USER" -g "$APP_USER" "${AGENT_DATA}/memory"

# ---------- 安装二进制（owner rwx, 其他 rx）----------
install -m 0755 -o "$APP_USER" -g "$APP_USER" "$BINARY" "$BIN_PATH"
echo "二进制已复制到 $BIN_PATH"

# ---------- 配置文件（仅 owner rw）----------
if [ -n "$CONFIG" ]; then
    install -m 0600 -o "$APP_USER" -g "$APP_USER" "$CONFIG" "$CONF_PATH"
    echo "配置文件已复制到 $CONF_PATH"
elif [ ! -f "$CONF_PATH" ]; then
    echo "警告: $CONF_PATH 不存在，请手动放置 config.yaml" >&2
fi

# ---------- systemd 单元 ----------
# 如果安装目录在 /home 下，不能开 ProtectHome（否则目录不可见）
PROTECT_HOME="true"
case "$APP_DIR" in
    /home/*|/root/*) PROTECT_HOME="false" ;;
esac

cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Mini-Agent Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${APP_USER}
Group=${APP_USER}
WorkingDirectory=${APP_DIR}
ExecStart=${BIN_PATH} -config ${CONF_PATH}
Restart=on-failure
RestartSec=5

# 安全加固
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=${PROTECT_HOME}
ReadWritePaths=${APP_DIR}
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
MemoryDenyWriteExecute=true
LockPersonality=true

# 资源限制
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}.service"
echo ""
echo "部署完成。目录结构："
echo "  ${APP_DIR}/"
echo "    agent          <- 二进制 (rwx)"
echo "    config.yaml    <- 配置   (rw, owner only)"
echo "    .agent/        <- 运行时数据 (rwx, owner only)"
echo "      sessions/"
echo "      memory/"
echo ""
echo "后续操作："
echo "  1. 确认 $CONF_PATH 配置正确"
echo "  2. systemctl start $SERVICE_NAME"
echo "  3. journalctl -u $SERVICE_NAME -f"
