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
	BlockCNNodes   []int  `json:"block_cn_nodes"` 
}

func GetCustomConfig(infos []*panel.NodeInfo) (*dns.Config, []*xray.OutboundHandlerConfig, *router.Config, error) {
	// --- DNS 初始化 ---
	queryStrategy := "UseIPv4v6"
	if !hasPublicIPv6() {
		queryStrategy = "UseIPv4"
	}

	var coreDnsConfig *coreConf.DNSConfig
	dnsFile := "/etc/v2node/dns.json"

	if _, err := os.Stat(dnsFile); err == nil {
		if content, err := os.ReadFile(dnsFile); err == nil {
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

	// --- 1. 读取并解析本地路由配置 ---
	localRouteFile := "/etc/v2node/route.json"
	localRoute := LocalRouteConfig{DomainStrategy: "AsIs"}
	if data, err := os.ReadFile(localRouteFile); err == nil {
		if err := json.Unmarshal(data, &localRoute); err == nil {
			log.Printf("[Route] 配置文件读取成功: %+v", localRoute)
		} else {
			log.Printf("[Route] 配置文件解析失败: %v", err)
		}
	}

	// --- 2. 初始化 Outbound 和 Router ---
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

	// --- 3. 核心屏蔽逻辑：带详细调试输出 ---
	log.Printf("[Route] 开始扫描节点信息，当前屏蔽列表: %v", localRoute.BlockCNNodes)
	for _, info := range infos {
		// 打印每个检测到的节点 ID，用于调试排查
		log.Printf("[Route] 检测到可用节点: Id=%d, Tag=%s", info.Id, info.Tag)
		
		isMatch := false
		for _, targetID := range localRoute.BlockCNNodes {
			if info.Id == targetID {
				isMatch = true
				break
			}
		}

		if isMatch {
			blockRule := map[string]interface{}{
				"type":        "field",
				"inboundTag":  []string{info.Tag}, 
				"source":      []string{"geoip:cn"},
				"outboundTag": "block",
			}
			rawBlockRule, _ := json.Marshal(blockRule)
			// 注入到最前端
			coreRouterConfig.RuleList = append([]json.RawMessage{rawBlockRule}, coreRouterConfig.RuleList...)
			log.Printf("[Route] 命中拦截规则！已为 Id %d 注入 [geoip:cn -> block] 路由", info.Id)
		}
	}

	// --- 4. 处理面板路由逻辑 ---
	for _, info := range infos {
		if len(info.Common.Routes) == 0 { continue }
		for _, route := range info.Common.Routes {
			switch route.Action {
			case "dns":
				if route.ActionValue == nil { continue }
				coreDnsConfig.Servers = append(coreDnsConfig.Servers, &coreConf.NameServerConfig{
					Address: &coreConf.Address{Address: xnet.ParseAddress(*route.ActionValue)},
					Domains: route.Match,
				})
			case "block", "block_ip", "block_port", "protocol":
				rule := map[string]interface{}{
					"inboundTag": []string{info.Tag}, "outboundTag": "block",
				}
				if route.Action == "block" { rule["domain"] = route.Match }
				if route.Action == "block_ip" { rule["ip"] = route.Match }
				if route.Action == "block_port" { rule["port"] = strings.Join(route.Match, ",") }
				if route.Action == "protocol" { rule["protocol"] = route.Match }
				raw, _ := json.Marshal(rule)
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, raw)
			case "route", "route_ip", "default_out":
				if route.ActionValue == nil { continue }
				outbound := &coreConf.OutboundDetourConfig{}
				if err := json.Unmarshal([]byte(*route.ActionValue), outbound); err == nil {
					rule := map[string]interface{}{
						"inboundTag": []string{info.Tag}, "outboundTag": outbound.Tag,
					}
					if route.Action == "route" { rule["domain"] = route.Match }
					if route.Action == "route_ip" { rule["ip"] = route.Match }
					if route.Action == "default_out" { rule["network"] = "tcp,udp" }
					raw, _ := json.Marshal(rule)
					coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, raw)
					if !hasOutboundWithTag(coreOutboundConfig, outbound.Tag) {
						if custom, err := outbound.Build(); err == nil {
							coreOutboundConfig = append(coreOutboundConfig, custom)
						}
					}
				}
			}
		}
	}

	DnsConfig, _ := coreDnsConfig.Build()
	RouterConfig, _ := coreRouterConfig.Build()
	return DnsConfig, coreOutboundConfig, RouterConfig, nil
}
