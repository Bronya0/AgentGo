#!/usr/bin/env bash
# restart.sh — 安全重启 mini-agent
#   支持两种模式:
#     1. 仅重启服务（无参数 / 仅指定安装目录）
#     2. 热更新二进制后重启（传入新二进制路径）
#
# 用法（root）:
#   bash restart.sh <安装根目录>                        # 仅重启
#   bash restart.sh <安装根目录> ./dist/agent-linux-amd64  # 替换二进制后重启

set -euo pipefail

APP_DIR="${1:?用法: restart.sh <安装根目录> [新二进制路径]}"
NEW_BINARY="${2:-}"

SERVICE_NAME="mini-agent"
BIN_PATH="${APP_DIR}/agent"

# ---------- 必须 root ----------
if [ "$(id -u)" -ne 0 ]; then
    echo "错误: 请使用 root 或 sudo 执行此脚本" >&2
    exit 1
fi

# ---------- 检查服务是否存在 ----------
if ! systemctl list-unit-files "${SERVICE_NAME}.service" &>/dev/null; then
    echo "错误: ${SERVICE_NAME}.service 不存在，请先执行 deploy.sh" >&2
    exit 1
fi

# ---------- 替换二进制（可选） ----------
if [ -n "$NEW_BINARY" ]; then
    if [ ! -f "$NEW_BINARY" ]; then
        echo "错误: 文件不存在 $NEW_BINARY" >&2
        exit 1
    fi

    BACKUP="${BIN_PATH}.bak"
    echo "备份旧二进制 -> $BACKUP"
    cp -f "$BIN_PATH" "$BACKUP"

    echo "安装新二进制 ..."
    install -m 0755 -o root -g root "$NEW_BINARY" "$BIN_PATH"
fi

# ---------- 优雅重启 ----------
echo "正在重启 ${SERVICE_NAME} ..."
systemctl restart "$SERVICE_NAME"

# ---------- 等待并检查 ----------
sleep 2
if systemctl is-active --quiet "$SERVICE_NAME"; then
    echo "重启成功 ✓"
    systemctl status "$SERVICE_NAME" --no-pager -l
else
    echo "警告: 服务未正常启动！" >&2
    journalctl -u "$SERVICE_NAME" --no-pager -n 20
    # 如果有备份，自动回滚
    if [ -n "$NEW_BINARY" ] && [ -f "${BIN_PATH}.bak" ]; then
        echo "回滚到旧二进制 ..."
        cp -f "${BIN_PATH}.bak" "$BIN_PATH"
        systemctl restart "$SERVICE_NAME"
        sleep 2
        if systemctl is-active --quiet "$SERVICE_NAME"; then
            echo "回滚成功 ✓"
        else
            echo "回滚后仍无法启动，请手动排查" >&2
            exit 1
        fi
    fi
    exit 1
fi
