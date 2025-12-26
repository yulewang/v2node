package core

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"strings"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/xtls/xray-core/app/dns"
	"github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	xray "github.com/xtls/xray-core/core"
	coreConf "github.com/xtls/xray-core/infra/conf"
)

// hasPublicIPv6 检查是否有公网 IPv6
func hasPublicIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.To4() == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsPrivate() {
			return true
		}
	}
	return false
}

func hasOutboundWithTag(list []*xray.OutboundHandlerConfig, tag string) bool {
	for _, o := range list {
		if o != nil && o.Tag == tag {
			return true
		}
	}
	return false
}

// 定义本地路由高级配置结构
type LocalRouteConfig struct {
	DomainStrategy string `json:"domainStrategy"`
	BlockCNNodes   []int  `json:"block_cn_nodes"` // 需要屏蔽大陆来源的节点 ID 列表
}

func GetCustomConfig(infos []*panel.NodeInfo) (*dns.Config, []*xray.OutboundHandlerConfig, *router.Config, error) {
	// --- DNS 策略初始化 ---
	queryStrategy := "UseIPv4v6"
	if !hasPublicIPv6() {
		queryStrategy = "UseIPv4"
	}

	var coreDnsConfig *coreConf.DNSConfig
	dnsFile := "/etc/v2node/dns.json"

	if _, err := os.Stat(dnsFile); err == nil {
		content, err := os.ReadFile(dnsFile)
		if err == nil {
			var externalDns coreConf.DNSConfig
			if err := json.Unmarshal(content, &externalDns); err == nil {
				log.Printf("[DNS] 成功加载配置 %s", dnsFile)
				coreDnsConfig = &externalDns
			}
		}
	}

	if coreDnsConfig == nil {
		coreDnsConfig = &coreConf.DNSConfig{
			Servers: []*coreConf.NameServerConfig{
				{Address: &coreConf.Address{Address: xnet.ParseAddress("localhost")}},
			},
			QueryStrategy: queryStrategy,
		}
	}

	// --- 1. 读取本地路由配置 ---
	localRouteFile := "/etc/v2node/route.json"
	localRoute := LocalRouteConfig{DomainStrategy: "AsIs"}
	if data, err := os.ReadFile(localRouteFile); err == nil {
		if err := json.Unmarshal(data, &localRoute); err == nil {
			log.Printf("[Route] 成功加载路由配置: %s", localRouteFile)
		}
	}

	// --- 初始化 Outbound 和 Router ---
	defaultoutbound, _ := buildDefaultOutbound()
	coreOutboundConfig := append([]*xray.OutboundHandlerConfig{}, defaultoutbound)
	block, _ := buildBlockOutbound()
	coreOutboundConfig = append(coreOutboundConfig, block)
	dnsOut, _ := buildDnsOutbound() 
	coreOutboundConfig = append(coreOutboundConfig, dnsOut)

	domainStrategy := localRoute.DomainStrategy 
	dnsRule, _ := json.Marshal(map[string]interface{}{
		"port": "53", "network": "udp", "outboundTag": "dns_out",
	})
	coreRouterConfig := &coreConf.RouterConfig{
		RuleList: []json.RawMessage{dnsRule},
		DomainStrategy: &domainStrategy,
	}

	// --- 2. 核心：通过 NodeId 匹配并注入屏蔽规则 ---
	if len(localRoute.BlockCNNodes) > 0 {
		for _, info := range infos {
			isMatch := false
			for _, id := range localRoute.BlockCNNodes {
				// 修复点：V2Board 的 NodeInfo 结构体中字段名为 NodeId
				if info.NodeId == id {
					isMatch = true
					break
				}
			}

			if isMatch {
				blockRule := map[string]interface{}{
					"type":        "field",
					"inboundTag":  []string{info.Tag}, 
					"source":      []string{"geoip:cn
