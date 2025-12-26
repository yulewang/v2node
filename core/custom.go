package core

import (
	"encoding/json"
	"fmt"
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
	DomainStrategy string   `json:"domainStrategy"`
	BlockCNPorts   []int    `json:"block_cn_ports"` // 需要屏蔽大陆来源的端口列表
}

func GetCustomConfig(infos []*panel.NodeInfo) (*dns.Config, []*xray.OutboundHandlerConfig, *router.Config, error) {
	// --- DNS 逻辑保持不变 ---
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

	// --- 读取本地路由配置 ---
	localRouteFile := "/etc/v2node/route.json"
	localRoute := LocalRouteConfig{DomainStrategy: "AsIs"}
	if data, err := os.ReadFile(localRouteFile); err == nil {
		json.Unmarshal(data, &localRoute)
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

	// --- 核心修复：通过 Tag 匹配端口并注入屏蔽规则 ---
	if len(localRoute.BlockCNPorts) > 0 {
		for _, info := range infos {
			isMatch := false
			for _, p := range localRoute.BlockCNPorts {
				// V2Board 的 Tag 格式通常包含端口号，如 "_11114"
				portSuffix := fmt.Sprintf("_%d", p)
				if strings.HasSuffix(info.Tag, portSuffix) {
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
				coreRouterConfig.RuleList = append([]json.RawMessage{rawBlockRule}, coreRouterConfig.RuleList...)
				log.Printf("[Route] 已通过 Tag 匹配为端口注入屏蔽大陆 IP 规则: %s", info.Tag)
			}
		}
	}

	// --- 面板常规规则处理逻辑 ---
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
