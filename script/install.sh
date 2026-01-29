#!/bin/bash

# 颜色定义
red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

# 检查root
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain} 必须使用root用户运行此脚本！\n" && exit 1

# 检查系统版本 (此处简化，逻辑保持一致)
# ... [系统检测逻辑] ...

########################
# 参数解析 (增强版：支持 --key value 和 --key=value)
########################
API_HOST_ARG=""
NODE_ID_ARG=""
API_KEY_ARG=""

# 在脚本最开始就进行解析，确保变量全局可用
while [[ $# -gt 0 ]]; do
    case "$1" in
        --api-host)
            API_HOST_ARG="$2"; shift 2 ;;
        --api-host=*)
            API_HOST_ARG="${1#*=}"; shift ;;
        --node-id)
            NODE_ID_ARG="$2"; shift 2 ;;
        --node-id=*)
            NODE_ID_ARG="${1#*=}"; shift ;;
        --api-key)
            API_KEY_ARG="$2"; shift 2 ;;
        --api-key=*)
            API_KEY_ARG="${1#*=}"; shift ;;
        *)
            shift ;;
    esac
done

# 打印调试信息 (你可以根据需要删掉这几行)
echo "--- 调试参数信息 ---"
echo "Host: ${API_HOST_ARG}"
echo "ID:   ${NODE_ID_ARG}"
echo "Key:  ${API_KEY_ARG}"
echo "--------------------"

# 架构检测
arch=$(uname -m)
case $arch in
    x86_64|x64|amd64) arch="64" ;;
    aarch64|arm64) arch="arm64-v8a" ;;
    *) arch="64" ;;
esac

install_base() {
    # 强制创建配置文件夹
    mkdir -p /etc/v2node
    if [[ -f /etc/debian_version ]]; then
        apt-get update && apt-get install -y wget curl unzip tar pv
    elif [[ -f /etc/redhat-release ]]; then
        yum install -y wget curl unzip tar epel-release pv
    elif [[ -f /etc/alpine-release ]]; then
        apk add --no-cache wget curl unzip tar pv
    fi
}

generate_v2node_config() {
    # 再次确保文件夹存在
    mkdir -p /etc/v2node
    
    echo -e "${yellow}正在写入配置文件...${plain}"
    cat > /etc/v2node/config.json <<EOF
{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "${API_HOST_ARG}",
            "NodeID": ${NODE_ID_ARG},
            "ApiKey": "${API_KEY_ARG}",
            "Timeout": 15
        }
    ]
}
EOF
    # 适配自定义逻辑的 dns.json
    if [[ ! -f /etc/v2node/dns.json ]]; then
        echo '{"servers":["localhost"]}' > /etc/v2node/dns.json
    fi
    echo -e "${green}配置文件生成成功: /etc/v2node/config.json${plain}"
}

install_v2node() {
    local repo="yulewang/v2node"
    mkdir -p /usr/local/v2node
    cd /usr/local/v2node

    # 获取版本
    last_version=$(curl -Ls "https://api.github.com/repos/${repo}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    [[ -z "$last_version" ]] && { echo "获取版本失败"; exit 1; }
    echo -e "${green}获取版本成功${last_version}${plain}"

    # 下载与解压
    wget -qO v2node-linux.zip "https://github.com/${repo}/releases/download/${last_version}/v2node-linux-${arch}.zip"
    unzip -o v2node-linux.zip && rm -f v2node-linux.zip
    chmod +x v2node
    cp -f geoip.dat geosite.dat /etc/v2node/ 2>/dev/null

    # Systemd 服务
    if [[ ! -f /etc/alpine-release ]]; then
        cat <<EOF > /etc/systemd/system/v2node.service
[Unit]
Description=v2node
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/usr/local/v2node/
ExecStart=/usr/local/v2node/v2node server
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload
        systemctl enable v2node
    fi

    # 【关键判断】判断变量是否捕获成功
    if [[ -n "$API_HOST_ARG" ]] && [[ -n "$NODE_ID_ARG" ]] && [[ -n "$API_KEY_ARG" ]]; then
        generate_v2node_config
        # 启动服务
        [[ -f /etc/alpine-release ]] && service v2node restart || systemctl restart v2node
    else
        echo -e "${red}检测到参数不完整，跳过自动生成配置过程。${plain}"
        echo -e "${yellow}请检查你的参数: --api-host, --node-id, --api-key${plain}"
    fi

    # 下载管理脚本
    curl -o /usr/bin/v2node -Ls "https://raw.githubusercontent.com/${repo}/yulewang-patch-1/script/v2node.sh"
    chmod +x /usr/bin/v2node
}

# 流程开始
install_base
install_v2node
