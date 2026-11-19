package engine

import (
	"encoding/json"
	"testing"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/stretchr/testify/require"
)

func TestDefaultPresetRoutesUnassignedLANThroughActiveBalancer(t *testing.T) {
	r := New(Options{LAN: "br-lan", TProxyPort: 10808})
	raw := `{"outbounds":[{"protocol":"socks","tag":"proxy"}],"routing":{"balancers":[{"tag":"balancer","selector":["proxy"]}],"rules":[{"type":"field","network":"tcp,udp","outboundTag":"proxy"}]}}`
	config, err := Build(raw, 10818, 10808, 10085, nil)
	require.NoError(t, err)
	data, err := r.buildRouting(config, RoutingPolicy{DefaultPreset: "Default", Presets: []api.RoutePreset{
		{Name: "Default", Action: "VPN", Rules: []api.RouteRule{{Action: "DIRECT", Ips: []string{"geoip:ru"}}}},
		{Name: "TV", Action: "BLOCK"},
	}})
	require.NoError(t, err)
	var result struct {
		Routing struct {
			DomainStrategy string
			Rules          []Object
		}
	}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, "IPOnDemand", result.Routing.DomainStrategy)
	require.Equal(t, []any{"manager-policy-in-0", "manager-tproxy"}, result.Routing.Rules[0]["inboundTag"])
	require.Equal(t, "manager-policy-direct", result.Routing.Rules[0]["outboundTag"])
	require.Equal(t, "balancer", result.Routing.Rules[1]["balancerTag"])
	require.Equal(t, []any{"manager-policy-in-1"}, result.Routing.Rules[2]["inboundTag"])
	require.Equal(t, "proxy", result.Routing.Rules[3]["outboundTag"])
}

func TestPresetRoutesZapretFromXrayOnly(t *testing.T) {
	r := New(Options{LAN: "br-lan", Mark: 1048576, TProxyPort: 10808, HealthURL: "https://example.com"})
	policy := RoutingPolicy{
		Queue: 200, UsesZapret: true,
		Presets: []api.RoutePreset{
			{Name: "TV", Macs: []string{"02:00:00:00:00:01"}, Action: "VPN", ProfileId: "tv", Fallback: "ZAPRET"},
			{Name: "PC", Ips: []string{"192.168.1.25/32"}, Action: "BLOCK", Fallback: "BLOCK"},
		},
		Profiles: map[string]string{"tv": `{"outbounds":[{"protocol":"vless","settings":{"vnext":[]}}]}`},
	}
	config, err := Build(`{"outbounds":[{"protocol":"freedom"}]}`, 10818, 10808, 10085, nil)
	require.NoError(t, err)
	data, err := r.buildRouting(config, policy)
	require.NoError(t, err)
	for _, value := range []string{"manager-policy-in-0", "manager-policy-in-1", "manager-policy-vpn-0-x", "manager-policy-zapret", "leastPing", "fallbackTag", "observatory"} {
		require.Contains(t, string(data), value)
	}
	r.SetRouting(policy)
	rules := r.firewall()
	require.Contains(t, rules, "ether saddr 02:00:00:00:00:01 meta l4proto { tcp, udp } tproxy to :20000")
	require.Contains(t, rules, "ip saddr 192.168.1.25/32 meta l4proto { tcp, udp } tproxy ip to 127.0.0.1:20001")
	require.Contains(t, rules, "meta mark 0x8000000 ct mark set ct mark | 0x8000000 queue num 200")
	require.Contains(t, rules, "ct direction reply ct mark & 0x8000000 == 0x8000000 queue num 200")
	require.NotContains(t, rules, "queue num 200 bypass")
}
