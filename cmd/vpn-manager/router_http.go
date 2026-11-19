package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
)

func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

var accessRoute = regexp.MustCompile(`\[(manager-(?:socks|tproxy|policy-in-[0-9]+))\s+(?:>>|->)\s+([^\]]+)\]`)

func (s server) GetRouterDashboard(w http.ResponseWriter, r *http.Request) {
	value := s.routerMetrics.snapshot()
	value.Outbounds = []api.RouterOutbound{}
	value.RoutingRules = []string{}
	value.Events = []api.RouterEvent{}
	if value.Storage == nil {
		value.Storage = []api.Storage{}
	}
	if value.DnsServers == nil {
		value.DnsServers = []string{}
	}
	if value.Connections.Destinations == nil {
		value.Connections.Destinations = []api.RouterDestination{}
	}
	telemetry := s.runtime.Telemetry()
	value.Icmp = s.routerMetrics.pingStatus(telemetry.Applied)
	state, _, _ := s.runtime.Snapshot()
	value.AppliedAt = timestamp(telemetry.Applied)
	value.ReconnectedAt = timestamp(telemetry.Reconnected)
	value.Reconnects = telemetry.Reconnects
	value.Crashes = telemetry.Crashes
	value.Health = api.RouterHealth{CheckedAt: timestamp(telemetry.Health.CheckedAt), Rtt: telemetry.Health.Milliseconds, Samples: telemetry.Health.Samples, Failed: telemetry.Health.Failed, Error: telemetry.Health.Error}
	if state == "running" {
		value.RoutingRules = append(value.RoutingRules, telemetry.Rules...)
	}
	_, outStats, statsAt := s.metrics.sample()
	value.StatsAvailable = state == "running" && outStats != nil && statsAt == telemetry.Applied
	counts := make(map[string]int)
	lines := s.runtime.Logs()
	for _, line := range lines {
		if match := accessRoute.FindStringSubmatch(line); len(match) > 0 {
			counts[match[2]]++
		}
	}
	if state == "running" {
		for _, out := range telemetry.Outbounds {
			category := "vpn"
			switch out.Protocol {
			case "freedom":
				category = "direct"
			case "blackhole":
				category = "blocked"
			case "dns", "loopback":
				category = "internal"
			}
			if out.Tag == "manager-policy-zapret" {
				category = "zapret"
			}
			traffic := engine.Traffic{}
			if value.StatsAvailable {
				traffic = outStats[out.Tag]
			}
			value.Outbounds = append(value.Outbounds, api.RouterOutbound{Tag: out.Tag, Protocol: out.Protocol, Address: out.Address, Category: category, Rx: traffic.Rx, Tx: traffic.Tx, ObservedConnections: counts[out.Tag]})
		}
	}
	for _, event := range telemetry.Events {
		value.Events = append(value.Events, api.RouterEvent{At: timestamp(event.At), Kind: event.Kind, Message: event.Message})
	}
	for _, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "dns") || strings.Contains(lower, "lookup")) && (strings.Contains(lower, "failed") || strings.Contains(lower, "error") || strings.Contains(lower, "timeout")) {
			value.Events = append(value.Events, api.RouterEvent{Kind: "dns", Message: line})
		}
	}
	value.Events = append(value.Events, s.routerMetrics.failedProbes()...)
	value.Events = value.Events[max(0, len(value.Events)-30):]
	activeID := s.active(r.Context())
	routing, _ := s.routingSettings(r.Context())
	profile := ""
	if activeID != "" {
		if record, err := s.queries.ProfileByID(r.Context(), activeID); err == nil {
			profile = record.Name
		}
	}
	for i := range value.Clients {
		client := &value.Clients[i]
		if routing.DeviceNames != nil {
			if alias := (*routing.DeviceNames)[client.Mac]; alias != "" {
				client.Alias = &alias
			}
		}
		if state != "running" || !s.runtime.TProxy {
			client.Profile = "Вне TProxy"
			continue
		}
		client.Profile = profile
		if name := matchPreset(routing.Presets, client.Mac, client.Addresses); name != "" {
			client.Profile = name
			continue
		}
		for _, mac := range s.runtime.BypassMACs {
			if strings.EqualFold(strings.ReplaceAll(mac, "-", ":"), client.Mac) {
				client.Profile = "Обход по MAC"
				break
			}
		}
		if client.Profile == profile && routing.DefaultPreset != nil && *routing.DefaultPreset != "" {
			client.Profile = *routing.DefaultPreset
		}
	}
	jsonResponse(w, value, nil)
}

func externalGet(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервис проверки вернул HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 16<<10))
}

func externalRoute(ctx context.Context, client *http.Client, mode string, family int) api.ExternalRoute {
	result := api.ExternalRoute{Mode: mode, Family: family}
	if client == nil {
		result.Error = "Xray остановлен"
		return result
	}
	endpoint := "https://api.ipify.org"
	if family == 6 {
		endpoint = "https://api6.ipify.org"
	}
	start := time.Now()
	raw, err := externalGet(ctx, client, endpoint)
	result.Latency = int(time.Since(start).Milliseconds())
	ip, parseErr := netip.ParseAddr(strings.TrimSpace(string(raw)))
	if err != nil || parseErr != nil || (family == 4) != ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		result.Error = "Нет ответа по этому протоколу"
		return result
	}
	result.Ip = ip.String()
	raw, err = externalGet(ctx, client, "https://ipwho.is/"+ip.String()+"?fields=success,country,connection")
	var geo struct {
		Success    bool
		Country    string
		Connection struct{ Asn int }
	}
	if err != nil || json.Unmarshal(raw, &geo) != nil || !geo.Success {
		result.GeoError = "Не удалось получить страну и ASN"
		return result
	}
	result.Country = geo.Country
	if geo.Connection.Asn > 0 {
		result.Asn = fmt.Sprintf("AS%d", geo.Connection.Asn)
	}
	return result
}

func (s server) CheckExternal(w http.ResponseWriter, r *http.Request) {
	if !s.externalMu.TryLock() {
		jsonResponse(w, nil, errors.New("проверка уже выполняется"))
		return
	}
	defer s.externalMu.Unlock()
	state, _, _ := s.runtime.Snapshot()
	applied := s.runtime.Telemetry().Applied
	if cached := s.externalCached; !cached.CheckedAt.IsZero() && time.Since(cached.CheckedAt) < 20*time.Second && cached.Applied == applied && cached.State == state {
		jsonResponse(w, cached.Result, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	direct := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var vpn *http.Client
	if state == "running" {
		vpn = s.runtime.VPNClient()
	}
	checked := api.ExternalCheck{Routes: make([]api.ExternalRoute, 4)}
	var tasks sync.WaitGroup
	for index, client := range []*http.Client{direct, vpn} {
		mode := "direct"
		if index == 1 {
			mode = "vpn"
		}
		for offset, family := range []int{4, 6} {
			tasks.Go(func() { checked.Routes[index*2+offset] = externalRoute(ctx, client, mode, family) })
		}
	}
	tasks.Wait()
	now := time.Now()
	checked.CheckedAt = timestamp(now)
	s.externalCached = externalCache{now, applied, state, checked}
	jsonResponse(w, checked, nil)
}

type externalCache struct {
	CheckedAt, Applied time.Time
	State              string
	Result             api.ExternalCheck
}
