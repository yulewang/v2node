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
# 参数解析 (已移除版本指定功能)
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
                shift ;; # 忽略所有无法识别的参数
        esac
    done
}

# 架构检测
arch=$(uname -m)
if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64-v8a"
elif [[ $arch == "s390x" ]]; then
    arch="s390x"
else
    arch="64"
    echo -e "${red}检测架构失败，使用默认架构: ${arch}${plain}"
fi

# 安装基础依赖
install_base() {
    if [[ x"${release}" == x"centos" ]]; then
        yum install -y wget curl unzip tar epel-release pv >/dev/null 2>&1
    elif [[ x"${release}" == x"alpine" ]]; then
        apk add --no-cache wget curl unzip tar pv >/dev/null 2>&1
    elif [[ x"${release}" == x"debian" || x"${release}" == x"ubuntu" ]]; then
        apt-get update -y >/dev/null 2>&1
        apt-get install -y wget curl unzip tar pv >/dev/null 2>&1
    elif [[ x"${release}" == x"arch" ]]; then
        pacman -Sy --noconfirm wget curl unzip tar pv >/dev/null 2>&1
    fi
    mkdir -p /etc/v2node
}

# 检查运行状态
check_status() {
    if [[ ! -f /usr/local/v2node/v2node ]]; then
        return 2
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        status=$(service v2node status 2>&1 | grep -E "started|running")
        [[ -n "$status" ]] && return 0 || return 1
    else
        status=$(systemctl is-active v2node)
        [[ x"${status}" == x"active" ]] && return 0 || return 1
    fi
}

# 生成配置文件
generate_v2node_config() {
    local api_host="$1"
    local node_id="$2"
    local api_key="$3"

    mkdir -p /etc/v2node >/dev/null 2>&1
    cat > /etc/v2node/config.json <<EOF
{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "${api_host}",
            "NodeID": ${node_id},
            "ApiKey": "${api_key}",
            "Timeout": 15
        }
    ]
}
EOF
    # 预留 dns.json 路径
    if [[ ! -f /etc/v2node/dns.json ]]; then
        echo '{"servers":["localhost"]}' > /etc/v2node/dns.json
    fi

    echo -e "${green}配置文件已生成：/etc/v2node/config.json${plain}"
    
    if [[ x"${release}" == x"alpine" ]]; then
        service v2node restart
    else
        systemctl restart v2node
    fi
    
    sleep 2
    if check_status; then
        echo -e "${green}v2node 启动/重启成功${plain}"
    else
        echo -e "${red}v2node 启动失败，请检查配置或使用 v2node log 查看日志${plain}"
    fi
}

install_v2node() {
    local repo_url="yulewang/v2node"

    if [[ -e /usr/local/v2node/ ]]; then
        rm -rf /usr/local/v2node/
    fi

    mkdir -p /usr/local/v2node/
    cd /usr/local/v2node/

    # 获取最新版本
    echo -e "${green}正在从 GitHub 获取最新版本信息...${plain}"
    last_version=$(curl -Ls "https://api.github.com/repos/${repo_url}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    
    if [[ ! -n "$last_version" ]]; then
        echo -e "${red}获取版本失败，请检查网络是否能访问 GitHub API${plain}"
        exit 1
    fi

    echo -e "${green}检测到最新版本：${last_version}，开始安装...${plain}"

    # 下载
    url="https://github.com/${repo_url}/releases/download/${last_version}/v2node-linux-${arch}.zip"
    curl -sL "$url" | pv -s 30M -W -N "下载进度" > /usr/local/v2node/v2node-linux.zip
    
    if [[ $? -ne 0 || ! -s v2node-linux.zip ]]; then
        echo -e "${red}下载二进制文件失败！${plain}"
        exit 1
    fi

    unzip -o v2node-linux.zip && rm -f v2node-linux.zip
    chmod +x v2node
    
    # 同步资源文件
    mkdir -p /etc/v2node
    [[ -f geoip.dat ]] && cp -f geoip.dat /etc/v2node/
    [[ -f geosite.dat ]] && cp -f geosite.dat /etc/v2node/

    # 服务安装
    if [[ x"${release}" == x"alpine" ]]; then
        cat <<EOF > /etc/init.d/v2node
#!/sbin/openrc-run
name="v2node"
command="/usr/local/v2node/v2node"
command_args="server"
command_user="root"
pidfile="/run/v2node.pid"
command_background="yes"
depend() { need net; }
EOF
        chmod +x /etc/init.d/v2node
        rc-update add v2node default
    else
        cat <<EOF > /etc/systemd/system/v2node.service
[Unit]
Description=v2node Service
After=network.target nss-lookup.target

[Service]
User=root
Type=simple
LimitNOFILE=999999
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

    # 自动化配置逻辑
    if [[ -n "$API_HOST_ARG" && -n "$NODE_ID_ARG" && -n "$API_KEY_ARG" ]]; then
        generate_v2node_config "$API_HOST_ARG" "$NODE_ID_ARG" "$API_KEY_ARG"
    else
        # 尝试启动已存在的配置
        if [[ -f /etc/v2node/config.json ]]; then
             [[ x"${release}" == x"alpine" ]] && service v2node start || systemctl start v2node
        fi
    fi

    # 安装管理脚本
    curl -o /usr/bin/v2node -Ls "https://raw.githubusercontent.com/${repo_url}/main/script/v2node.sh"
    chmod +x /usr/bin/v2node

    echo -e "${green}v2node ${last_version} 安装成功。${plain}"
}

# 执行主流程
parse_args "$@"
install_base
install_v2node
