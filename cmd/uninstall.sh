#!/bin/bash

# ====== 配置参数（需与 install.sh 一致） ======
APP_NAME="fops-agent"
INSTALL_DIR="/usr/local/bin"
SERVICE_FILE="/etc/systemd/system/${APP_NAME}.service"
ENV_FILE="/etc/profile.d/${APP_NAME}.sh"

# ====== 卸载逻辑 ======
# 检查 root 权限
if [ "$(id -u)" != "0" ]; then
   echo "请使用 sudo 运行此脚本" >&2
   exit 1
fi

# 停止并禁用服务
echo "停止服务..."
systemctl stop "${APP_NAME}" 2>/dev/null
systemctl disable "${APP_NAME}" 2>/dev/null

# 删除服务文件
echo "删除服务文件 ${SERVICE_FILE}..."
rm -f "${SERVICE_FILE}"
systemctl daemon-reload
systemctl reset-failed

# 删除二进制文件
echo "删除程序文件 ${INSTALL_DIR}/${APP_NAME}..."
rm -f "${INSTALL_DIR}/${APP_NAME}"

# 删除环境变量
echo "删除环境变量文件 ${ENV_FILE}..."
rm -f "${ENV_FILE}"

# 可选：删除数据目录（按需启用）
# read -p "是否删除数据目录 /var/lib/${APP_NAME}？[y/N] " -n 1 -r
# if [[ $REPLY =~ ^[Yy]$ ]]; then
#   rm -rf "/var/lib/${APP_NAME}"
# fi

# 可选：删除日志文件（按需启用）
# rm -f "/var/log/${APP_NAME}.log"
# rm -f "/var/log/${APP_NAME}-error.log"

echo "卸载完成！"