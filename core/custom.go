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

func GetCustomConfig(infos []*panel.NodeInfo) (*dns.Config, []*xray.OutboundHandlerConfig, *router.Config, error) {
	// --- DNS 策略初始化 ---
	queryStrategy := "UseIPv4v6"
	if !hasPublicIPv6() {
		queryStrategy = "UseIPv4"
	}

	var coreDnsConfig *coreConf.DNSConfig
	dnsFile := "/etc/v2node/dns.json"

	// 1. 优先尝试读取外部配置文件
	if _, err := os.Stat(dnsFile); err == nil {
		content, err := os.ReadFile(dnsFile)
		if err == nil {
			var externalDns coreConf.DNSConfig
			if err := json.Unmarshal(content, &externalDns); err == nil {
				log.Printf("[DNS] 成功加载外部配置 %s，已跳过面板默认 localhost", dnsFile)
				coreDnsConfig = &externalDns
				if coreDnsConfig.QueryStrategy == "" {
					coreDnsConfig.QueryStrategy = queryStrategy
				}
			} else {
				log.Printf("[DNS] 警告: %s 解析失败: %v", dnsFile, err)
			}
		}
	}

	// 2. 如果没有外部文件，执行原始逻辑（含 localhost）
	if coreDnsConfig == nil {
		coreDnsConfig = &coreConf.DNSConfig{
			Servers: []*coreConf.NameServerConfig{
				{
					Address: &coreConf.Address{
						Address: xnet.ParseAddress("localhost"),
					},
				},
			},
			QueryStrategy: queryStrategy,
		}
	}

	// --- Outbound 初始化 ---
	defaultoutbound, _ := buildDefaultOutbound()
	coreOutboundConfig := append([]*xray.OutboundHandlerConfig{}, defaultoutbound)
	block, _ := buildBlockOutbound()
	coreOutboundConfig = append(coreOutboundConfig, block)
	dnsOut, _ := buildDnsOutbound() // 这里重命名，避免遮蔽包名
	coreOutboundConfig = append(coreOutboundConfig, dnsOut)

	// --- Router 初始化 ---
	domainStrategy := "AsIs"
	dnsRule, _ := json.Marshal(map[string]interface{}{
		"port":        "53",
		"network":     "udp",
		"outboundTag": "dns_out",
	})
	coreRouterConfig := &coreConf.RouterConfig{
		RuleList:       []json.RawMessage{dnsRule},
		DomainStrategy: &domainStrategy,
	}

	// --- 处理面板规则 ---
	for _, info := range infos {
		if len(info.Common.Routes) == 0 {
			continue
		}
		for _, route := range info.Common.Routes {
			switch route.Action {
			case "dns":
				if route.ActionValue == nil {
					continue
				}
				server := &coreConf.NameServerConfig{
					Address: &coreConf.Address{
						Address: xnet.ParseAddress(*route.ActionValue),
					},
					Domains: route.Match,
				}
				coreDnsConfig.Servers = append(coreDnsConfig.Servers, server)

			case "block", "block_ip", "block_port", "protocol":
				rule := map[string]interface{}{
					"inboundTag":  []string{info.Tag}, // 必须是 []string
					"outboundTag": "block",
				}
				if route.Action == "block" { rule["domain"] = route.Match }
				if route.Action == "block_ip" { rule["ip"] = route.Match }
				if route.Action == "block_port" { rule["port"] = strings.Join(route.Match, ",") }
				if route.Action == "protocol" { rule["protocol"] = route.Match }
				
				rawRule, _ := json.Marshal(rule)
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)

			case "route", "route_ip", "default_out":
				if route.ActionValue == nil {
					continue
				}
				outbound := &coreConf.OutboundDetourConfig{}
				if err := json.Unmarshal([]byte(*route.ActionValue), outbound); err != nil {
					continue
				}
				rule := map[string]interface{}{
					"inboundTag":  []string{info.Tag}, // 必须是 []string
					"outboundTag": outbound.Tag,
				}
				if route.Action == "route" { rule["domain"] = route.Match }
				if route.Action == "route_ip" { rule["ip"] = route.Match }
				if route.Action == "default_out" { rule["network"] = "tcp,udp" }

				rawRule, _ := json.Marshal(rule)
				coreRouterConfig.RuleList = append(coreRouterConfig.RuleList, rawRule)
				
				if !hasOutboundWithTag(coreOutboundConfig, outbound.Tag) {
					if custom_outbound, err := outbound.Build(); err == nil {
						coreOutboundConfig = append(coreOutboundConfig, custom_outbound)
					}
				}
			}
		}
	}

	// --- 最终构建 ---
	DnsConfig, err := coreDnsConfig.Build()
	if err != nil {
		return nil, nil, nil, err
	}
	RouterConfig, err := coreRouterConfig.Build()
	if err != nil {
		return nil, nil, nil, err
	}
	return DnsConfig, coreOutboundConfig, RouterConfig, nil
}
