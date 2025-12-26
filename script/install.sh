#!/bin/bash

# 颜色定义
red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)

# 检查root
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain} 必须使用root用户运行此脚本！\n" && exit 1

# 检查系统版本
if [[ -f /etc/redhat-release ]]; then
    release="centos"
elif cat /etc/issue | grep -Eqi "alpine"; then
    release="alpine"
elif cat /etc/issue | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "arch"; then
    release="arch"
else
    echo -e "${red}未检测到系统版本，请联系脚本作者！${plain}\n" && exit 1
fi

########################
# 参数解析
########################
API_HOST_ARG=""
NODE_ID_ARG=""
API_KEY_ARG=""

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --api-host)
                API_HOST_ARG="$2"; shift 2 ;;
            --node-id)
                NODE_ID_ARG="$2"; shift 2 ;;
            --api-key)
                API_KEY_ARG="$2"; shift 2 ;;
            *)
                shift ;;
        esac
    done
}

# 架构检测
arch=$(uname -m)
if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64-v8a"
else
    arch="64"
fi

install_base() {
    if [[ x"${release}" == x"centos" ]]; then
        yum install -y wget curl unzip tar epel-release pv >/dev/null 2>&1
    elif [[ x"${release}" == x"alpine" ]]; then
        apk add --no-cache wget curl unzip tar pv >/dev/null 2>&1
    else
        apt-get update -y >/dev/null 2>&1
        apt-get install -y wget curl unzip tar pv >/dev/null 2>&1
    fi
    mkdir -p /etc/v2node
}

check_status() {
    if [[ ! -f /usr/local/v2node/v2node ]]; then return 2; fi
    if [[ x"${release}" == x"alpine" ]]; then
        service v2node status 2>&1 | grep -E "started|running" >/dev/null 2>&1
        return $?
    else
        [[ $(systemctl is-active v2node) == "active" ]] && return 0 || return 1
    fi
}

generate_v2node_config() {
    local host=$1
    local id=$2
    local key=$3

    echo -e "${yellow}正在生成配置文件...${plain}"
    mkdir -p /etc/v2node
    cat > /etc/v2node/config.json <<EOF
{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "${host}",
            "NodeID": ${id},
            "ApiKey": "${key}",
            "Timeout": 15
        }
    ]
}
EOF
    # 确保 dns.json 存在以适配自定义逻辑
    if [[ ! -f /etc/v2node/dns.json ]]; then
        echo '{"servers":["localhost"]}' > /etc/v2node/dns.json
    fi

    if [[ x"${release}" == x"alpine" ]]; then
        service v2node restart
    else
        systemctl restart v2node
    fi
    
    sleep 2
    if check_status; then
        echo -e "${green}v2node 启动成功并已应用配置。${plain}"
    else
        echo -e "${red}v2node 启动失败，请检查参数是否正确。${plain}"
    fi
}

install_v2node() {
    local repo="yulewang/v2node"
    mkdir -p /usr/local/v2node
    cd /usr/local/v2node

    last_version=$(curl -Ls "https://api.github.com/repos/${repo}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    if [[ -z "$last_version" ]]; then echo "无法获取版本"; exit 1; fi

    echo -e "${green}开始下载 v2node ${last_version}...${plain}"
    url="https://github.com/${repo}/releases/download/${last_version}/v2node-linux-${arch}.zip"
    curl -sL "$url" | pv -s 30M -W -N "下载进度" > v2node-linux.zip
    
    unzip -o v2node-linux.zip && rm -f v2node-linux.zip
    chmod +x v2node
    cp -f geoip.dat geosite.dat /etc/v2node/ 2>/dev/null

    # 写入服务文件
    if [[ x"${release}" != x"alpine" ]]; then
        cat <<EOF > /etc/systemd/system/v2node.service
[Unit]
Description=v2node Service
After=network.target

[Service]
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

    # 核心：判断是否执行配置生成
    echo "--------------------------------"
    echo "检测到参数状态："
    echo "API Host: ${API_HOST_ARG:-未设置}"
    echo "Node ID:  ${NODE_ID_ARG:-未设置}"
    echo "API Key:  ${API_KEY_ARG:-未设置}"
    echo "--------------------------------"

    if [[ -n "$API_HOST_ARG" && -n "$NODE_ID_ARG" && -n "$API_KEY_ARG" ]]; then
        generate_v2node_config "$API_HOST_ARG" "$NODE_ID_ARG" "$API_KEY_ARG"
    else
        echo -e "${yellow}缺少必要参数，跳过自动配置步骤。${plain}"
        if [[ -f /etc/v2node/config.json ]]; then
            [[ x"${release}" == x"alpine" ]] && service v2node start || systemctl start v2node
        fi
    fi

    # 下载管理脚本
    curl -o /usr/bin/v2node -Ls "https://raw.githubusercontent.com/${repo}/main/script/v2node.sh"
    chmod +x /usr/bin/v2node
    echo -e "${green}安装完成。${plain}"
}

parse_args "$@"
install_base
install_v2node
