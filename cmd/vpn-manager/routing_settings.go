package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
)

const routingKey = "routing_policy"

func matchPreset(presets []api.RoutePreset, mac string, addresses []string) string {
	for _, preset := range presets {
		if slices.Contains(preset.Macs, mac) {
			return preset.Name
		}
	}
	for _, preset := range presets {
		for _, value := range preset.Ips {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				continue
			}
			for _, address := range addresses {
				ip, err := netip.ParseAddr(address)
				if err == nil && prefix.Contains(ip.Unmap()) {
					return preset.Name
				}
			}
		}
	}
	return ""
}

func (m *manager) routingSettings(ctx context.Context) (api.RoutingSettings, error) {
	cfg := api.RoutingSettings{Queue: 200}
	err := m.readSetting(ctx, routingKey, &cfg)
	if len(cfg.Presets) == 0 && cfg.DefaultPreset == nil {
		name := "VPN RU DIRECT"
		cfg.DefaultPreset = &name
		cfg.Presets = []api.RoutePreset{{
			Name: name, Action: "VPN", Fallback: "ZAPRET", Macs: []string{}, Ips: []string{},
			Rules: []api.RouteRule{
				{Action: "DIRECT", Domains: []string{}, Ips: []string{}, Protocols: []string{"bittorrent"}},
				{Action: "DIRECT", Domains: strings.Fields("domain:ru domain:xn--p1ai domain:su domain:2gis.com domain:vk.com domain:vkuseraudio.net domain:vkuser.net domain:yastatic.net domain:yandex.net domain:wildberries.net domain:wb.ru geosite:category-ru domain:ozonusercontent.com domain:gekai.net"), Ips: []string{}, Protocols: []string{}},
				{Action: "DIRECT", Ips: []string{"geoip:ru"}, Domains: []string{}, Protocols: []string{}},
			},
		},
			{Name: "Zapret2", Action: "DIRECT", Fallback: "DIRECT", Macs: []string{}, Ips: []string{}, Rules: defaultZapretRules()},
			{Name: "Direct", Action: "DIRECT", Fallback: "DIRECT", Macs: []string{}, Ips: []string{}, Rules: []api.RouteRule{}},
		}
	}
	return cfg, err
}

func defaultZapretRules() []api.RouteRule {
	domains := []string{
		"domain:youtube.com", "domain:youtu.be", "domain:youtube-nocookie.com",
		"domain:googlevideo.com", "domain:ytimg.com", "domain:speedtest.net",
		"domain:discord.com", "domain:discord.gg", "domain:discordapp.com",
		"domain:discordapp.net", "domain:discord.media",
	}
	return []api.RouteRule{
		{Action: "ZAPRET", Domains: []string{}, Ips: []string{"162.159.130.235/32"}, Protocols: []string{}, Network: "tcp", Ports: "2053"},
		{Action: "ZAPRET", Domains: domains, Ips: []string{}, Protocols: []string{}, Network: "tcp", Ports: "80,443,2053"},
		{Action: "ZAPRET", Domains: domains, Ips: []string{}, Protocols: []string{}, Network: "udp", Ports: "443"},
		{Action: "ZAPRET", Domains: []string{}, Ips: []string{}, Protocols: []string{}, Network: "udp", Ports: "19294-19344,50000-50099"},
	}
}

var validName = regexp.MustCompile(`^[^\p{C}]{1,80}$`)
var validTag = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,128}$`)

func routeAction(value string, vpn bool) bool {
	return value == "DIRECT" || value == "ZAPRET" || value == "BLOCK" || (vpn && value == "VPN")
}

func routingPrefix(value string) (string, error) {
	value = strings.TrimSpace(value)
	if addr, err := netip.ParseAddr(value); err == nil && addr.Zone() == "" {
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()).String(), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Addr().Is4In6() {
		return "", fmt.Errorf("некорректный IP/CIDR %q", value)
	}
	return prefix.Masked().String(), nil
}

func validatePorts(value string) bool {
	if value == "" {
		return true
	}
	parse := func(s string) int {
		n, _ := strconv.Atoi(s)
		if n < 1 || n > 65535 {
			return 0
		}
		return n
	}
	for _, token := range strings.Split(value, ",") {
		left, right, ranged := strings.Cut(token, "-")
		first := parse(left)
		if first == 0 || (ranged && parse(right) < first) {
			return false
		}
	}
	return true
}

func (m *manager) prepareRouting(ctx context.Context, cfg *api.RoutingSettings) (engine.RoutingPolicy, error) {
	return m.prepareRoutingWithZapret(ctx, cfg, nil)
}

func (m *manager) prepareRoutingWithZapret(ctx context.Context, cfg *api.RoutingSettings, override *zapretState) (engine.RoutingPolicy, error) {
	plan := engine.RoutingPolicy{Profiles: make(map[string]string), Queue: cfg.Queue}
	if cfg.DefaultPreset != nil {
		plan.DefaultPreset = *cfg.DefaultPreset
	}
	if cfg.DeviceNames != nil {
		if len(*cfg.DeviceNames) > 256 {
			return plan, errors.New("можно сохранить до 256 имён устройств")
		}
		for address, name := range *cfg.DeviceNames {
			mac, err := net.ParseMAC(address)
			if err != nil || len(mac) != 6 || mac.String() != address || !validName.MatchString(name) {
				return plan, errors.New("некорректный MAC или имя устройства")
			}
		}
	}
	if cfg.Queue < 0 || cfg.Queue > 65535 || len(cfg.Presets) > 16 {
		return plan, errors.New("максимум 16 пресетов; NFQUEUE от 0 до 65535")
	}
	names, macs, ips := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range cfg.Presets {
		preset := &cfg.Presets[i]
		preset.Name = strings.TrimSpace(preset.Name)
		if !validName.MatchString(preset.Name) || names[preset.Name] {
			return plan, errors.New("у каждого пресета должно быть уникальное имя")
		}
		names[preset.Name] = true
		if len(preset.Macs)+len(preset.Ips) > 64 || len(preset.Rules) > 64 {
			return plan, fmt.Errorf("%s: можно сохранить до 64 привязок и 64 правил", preset.Name)
		}
		for index, value := range preset.Macs {
			mac, err := net.ParseMAC(strings.TrimSpace(value))
			if err != nil || len(mac) != 6 || macs[mac.String()] {
				return plan, fmt.Errorf("%s: некорректный или повторный MAC %q", preset.Name, value)
			}
			preset.Macs[index] = mac.String()
			macs[mac.String()] = true
		}
		for index, value := range preset.Ips {
			prefix, err := routingPrefix(value)
			if err != nil || ips[prefix] {
				return plan, fmt.Errorf("%s: некорректный или повторный IP/CIDR %q", preset.Name, value)
			}
			preset.Ips[index] = prefix
			ips[prefix] = true
		}
		if !routeAction(preset.Action, true) || !routeAction(preset.Fallback, true) {
			return plan, fmt.Errorf("%s: проверьте действия пресета", preset.Name)
		}
		if preset.Fallback == "VPN" && preset.ProfileId == "" {
			return plan, fmt.Errorf("%s: для запасного действия VPN выберите VPN-профиль", preset.Name)
		}
		vpn := preset.Action == "VPN"
		usesZapret := preset.Action == "ZAPRET"
		if preset.OutboundTag != "" && !validTag.MatchString(preset.OutboundTag) {
			return plan, errors.New("некорректный тег outbound")
		}
		for _, rule := range preset.Rules {
			if !routeAction(rule.Action, true) || (rule.OutboundTag != "" && !validTag.MatchString(rule.OutboundTag)) ||
				(rule.Network != "" && rule.Network != "tcp" && rule.Network != "udp" && rule.Network != "tcp,udp") || !validatePorts(rule.Ports) {
				return plan, fmt.Errorf("%s: проверьте действие, тег, сеть или порты правила", preset.Name)
			}
			if len(rule.Domains)+len(rule.Ips)+len(rule.Protocols) == 0 && rule.Ports == "" && rule.Network == "" {
				return plan, fmt.Errorf("%s: у правила должно быть хотя бы одно условие", preset.Name)
			}
			vpn = vpn || rule.Action == "VPN"
			usesZapret = usesZapret || rule.Action == "ZAPRET"
		}
		if preset.Name != plan.DefaultPreset && len(preset.Macs)+len(preset.Ips) == 0 {
			continue
		}
		plan.UsesZapret = plan.UsesZapret || usesZapret || (vpn && preset.Fallback == "ZAPRET")
		if preset.ProfileId != "" {
			record, err := m.queries.ProfileByID(ctx, preset.ProfileId)
			if err != nil {
				return plan, fmt.Errorf("%s: VPN-профиль недоступен", preset.Name)
			}
			plan.Profiles[preset.ProfileId] = record.Config
		}
		plan.Presets = append(plan.Presets, *preset)
	}
	if plan.DefaultPreset != "" && !names[plan.DefaultPreset] {
		return plan, errors.New("пресет для общих правил отсутствует")
	}
	if plan.UsesZapret {
		state := override
		if state == nil {
			saved, err := m.loadZapret(ctx)
			if err != nil {
				return plan, err
			}
			state = &saved
		}
		plan.UsesZapret = state.Enabled
		if state.Enabled {
			settings, err := m.effectiveZapret(*state)
			if err != nil {
				return plan, err
			}
			plan.Zapret = settings
		}
	}
	return plan, nil
}

func (m *manager) applyProfile(ctx context.Context, raw string) error {
	cfg, err := m.routingSettings(ctx)
	if err != nil {
		return err
	}
	plan, err := m.prepareRouting(ctx, &cfg)
	if err != nil {
		return err
	}
	return m.applyRoutingConfig(ctx, raw, plan)
}

func (m *manager) applyRoutingConfig(ctx context.Context, raw string, plan engine.RoutingPolicy) error {
	plan.DirectOnly = raw == directXrayConfig
	return m.runtime.ApplyRouting(ctx, raw, plan)
}

func (s server) GetRoutingSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.routingSettings(r.Context())
	jsonResponse(w, cfg, err)
}

func (s server) PutRoutingSettings(w http.ResponseWriter, r *http.Request) {
	var cfg api.RoutingSettings
	if !decode(w, r, &cfg) {
		return
	}
	if cfg.Presets == nil {
		cfg.Presets = []api.RoutePreset{}
	}
	if cfg.DefaultPreset == nil && len(cfg.Presets) == 0 {
		noDefault := ""
		cfg.DefaultPreset = &noDefault
	}
	ctx := r.Context()
	s.writes.Lock()
	defer s.writes.Unlock()
	old, err := s.routingSettings(ctx)
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	plan, err := s.prepareRouting(ctx, &cfg)
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	active := s.active(ctx)
	raw := ""
	routesChanged := old.Queue != cfg.Queue || !reflect.DeepEqual(old.DefaultPreset, cfg.DefaultPreset) || !reflect.DeepEqual(old.Presets, cfg.Presets)
	if s.xrayWanted(ctx) && s.runtime.TProxy && routesChanged {
		raw, err = s.xrayConfig(ctx, active)
		if err != nil {
			jsonResponse(w, nil, err)
			return
		}
		if err = s.applyRoutingConfig(ctx, raw, plan); err != nil {
			jsonResponse(w, nil, err)
			return
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	err = s.writeSetting(persist, routingKey, cfg)
	if err != nil && raw != "" {
		previous, prepareErr := s.prepareRouting(persist, &old)
		if prepareErr == nil {
			err = errors.Join(err, s.applyRoutingConfig(persist, raw, previous))
		} else {
			err = errors.Join(err, prepareErr)
		}
	}
	jsonResponse(w, cfg, err)
}
