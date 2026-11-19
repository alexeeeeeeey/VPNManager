package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
)

const zapretMark = 0x08000000
const zapretGeneratedMark = 0x40000000

type RoutingPolicy struct {
	DefaultPreset string
	DirectOnly    bool
	Presets       []api.RoutePreset
	Profiles      map[string]string
	Queue         int
	Zapret        api.ZapretSettings
	UsesZapret    bool
}

func (r *Runtime) SetRouting(policy RoutingPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routing = policy
}

func (r *Runtime) ApplyRouting(ctx context.Context, raw string, policy RoutingPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.publish()
	previous := r.routing
	r.routingChanged = true
	defer func() { r.routingChanged = false }()
	r.routing = policy
	if err := r.apply(ctx, raw); err != nil {
		r.routing = previous
		return err
	}
	return nil
}

func (r *Runtime) policyPort(index int) int {
	port := 20000
	for {
		if port != r.StatsPort && port != r.TProxyPort && port != r.SocksPort && port != r.resolverPort {
			if index == 0 {
				return port
			}
			index--
		}
		port++
	}
}

func (r *Runtime) buildRouting(data []byte, policy RoutingPolicy) ([]byte, error) {
	if len(policy.Presets) == 0 {
		return data, nil
	}
	var cfg Object
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	inbounds, _ := cfg["inbounds"].([]any)
	outbounds, _ := cfg["outbounds"].([]any)
	routing, _ := cfg["routing"].(map[string]any)
	if routing == nil {
		routing = Object{}
	}
	previous, _ := routing["rules"].([]any)
	balancers, _ := routing["balancers"].([]any)
	var activeBalancer, activeOutbound string
	for _, item := range balancers {
		if value, ok := item.(map[string]any); ok {
			tag, _ := value["tag"].(string)
			if activeBalancer == "" || tag == "balancer" {
				activeBalancer = tag
			}
		}
	}
	tags := make(map[string]bool)
	protocols := make(map[string]string)
	for _, outbound := range outbounds {
		if value, ok := outbound.(map[string]any); ok {
			if tag, ok := value["tag"].(string); ok {
				tags[tag] = true
				protocols[tag], _ = value["protocol"].(string)
				if activeOutbound == "" && vpnProtocol(protocols[tag]) {
					activeOutbound = tag
				}
			}
		}
	}
	for _, tag := range []string{"manager-policy-direct", "manager-policy-zapret", "manager-policy-block"} {
		if tags[tag] {
			return nil, fmt.Errorf("зарезервированный тег outbound %s", tag)
		}
	}
	outbounds = append(outbounds, Object{"tag": "manager-policy-direct", "protocol": "freedom"}, Object{"tag": "manager-policy-block", "protocol": "blackhole"})
	if policy.UsesZapret {
		outbounds = append(outbounds, Object{"tag": "manager-policy-zapret", "protocol": "freedom", "streamSettings": Object{"sockopt": Object{"mark": zapretMark}}})
		inbounds = append(inbounds, Object{"tag": "manager-policy-zapret-test", "listen": "127.0.0.1", "port": r.policyPort(16), "protocol": "socks"})
	}
	profileTags := map[string]string{}
	observed := map[string]bool{}
	var generated []any
	if policy.UsesZapret {
		generated = append(generated, Object{"type": "field", "inboundTag": []string{"manager-policy-zapret-test"}, "outboundTag": "manager-policy-zapret"})
	}
	for index, preset := range policy.Presets {
		tag := fmt.Sprintf("manager-policy-in-%d", index)
		inboundTags := []string{tag}
		if preset.Name == policy.DefaultPreset {
			inboundTags = append(inboundTags, "manager-tproxy")
		}
		inbounds = append(inbounds, Object{
			"tag": tag, "port": r.policyPort(index), "protocol": "dokodemo-door",
			"settings":       Object{"network": "tcp,udp", "followRedirect": true},
			"sniffing":       Object{"enabled": true, "destOverride": []string{"http", "tls", "quic", "fakedns"}, "routeOnly": true},
			"streamSettings": Object{"sockopt": Object{"tproxy": "tproxy"}},
		})
		profileTag := func() (string, error) {
			if tag := profileTags[preset.ProfileId]; tag != "" {
				return tag, nil
			}
			outbound, err := policyProfileOutbound(policy.Profiles[preset.ProfileId])
			if err != nil {
				return "", fmt.Errorf("%s: %w", preset.Name, err)
			}
			tag := fmt.Sprintf("manager-policy-vpn-%d-x", index)
			if tags[tag] {
				return "", errors.New("зарезервированный тег VPN уже используется")
			}
			outbound["tag"] = tag
			tags[tag] = true
			profileTags[preset.ProfileId] = tag
			outbounds = append(outbounds, outbound)
			return tag, nil
		}
		fallback := policyTag(preset.Fallback, policy.UsesZapret)
		if preset.Fallback == "VPN" {
			var err error
			fallback, err = profileTag()
			if err != nil {
				return nil, err
			}
		}
		rules := append(append([]api.RouteRule(nil), preset.Rules...), api.RouteRule{Action: preset.Action, OutboundTag: preset.OutboundTag})
		for ruleIndex, rule := range rules {
			target := Object{"type": "field", "inboundTag": inboundTags}
			if len(rule.Domains) > 0 {
				target["domain"] = rule.Domains
			}
			if len(rule.Ips) > 0 {
				target["ip"] = rule.Ips
			}
			if len(rule.Protocols) > 0 {
				target["protocol"] = rule.Protocols
			}
			if rule.Network != "" {
				target["network"] = rule.Network
			}
			if rule.Ports != "" {
				target["port"] = rule.Ports
			}
			if rule.Action != "VPN" {
				action := rule.Action
				if action == "ZAPRET" && !policy.UsesZapret {
					target["outboundTag"] = fallback
				} else {
					target["outboundTag"] = policyTag(action, policy.UsesZapret)
				}
			} else {
				outboundTag := rule.OutboundTag
				if outboundTag == "" {
					outboundTag = preset.OutboundTag
				}
				if outboundTag == "" && preset.ProfileId == "" {
					if policy.DirectOnly {
						target["outboundTag"] = "manager-policy-direct"
						generated = append(generated, target)
						continue
					}
					if activeBalancer != "" {
						target["balancerTag"] = activeBalancer
						generated = append(generated, target)
						continue
					}
					outboundTag = activeOutbound
					if outboundTag == "" {
						target["outboundTag"] = "manager-policy-block"
						generated = append(generated, target)
						continue
					}
				}
				if outboundTag == "" {
					var err error
					outboundTag, err = profileTag()
					if err != nil {
						return nil, err
					}
				} else if policy.DirectOnly && !vpnProtocol(protocols[outboundTag]) {
					target["outboundTag"] = fallback
					generated = append(generated, target)
					continue
				} else if !tags[outboundTag] {
					return nil, fmt.Errorf("%s: outbound %q отсутствует в активной конфигурации", preset.Name, outboundTag)
				} else if !vpnProtocol(protocols[outboundTag]) {
					return nil, fmt.Errorf("%s: outbound %q не является VPN", preset.Name, outboundTag)
				}
				// Xray selectors match prefixes. Refuse ambiguous selectors instead of using the wrong VPN.
				for candidate := range tags {
					if candidate != outboundTag && strings.HasPrefix(candidate, outboundTag) {
						return nil, fmt.Errorf("тег %q неоднозначен для проверки VPN", outboundTag)
					}
				}
				observed[outboundTag] = true
				balancer := fmt.Sprintf("manager-policy-bal-%d-%d", index, ruleIndex)
				fallbackTag := fallback
				if fallbackTag == outboundTag {
					fallbackTag = "manager-policy-block"
				}
				balancers = append(balancers, Object{"tag": balancer, "selector": []string{outboundTag},
					"fallbackTag": fallbackTag, "strategy": Object{"type": "leastPing"}})
				target["balancerTag"] = balancer
			}
			generated = append(generated, target)
		}
	}
	if len(observed) > 0 {
		if _, exists := cfg["burstObservatory"]; exists {
			return nil, errors.New("профиль использует burstObservatory; для пресетов выберите профиль без него")
		}
		observatory, _ := cfg["observatory"].(map[string]any)
		if observatory == nil {
			observatory = Object{}
		}
		selectors, _ := observatory["subjectSelector"].([]any)
		for tag := range observed {
			selectors = append(selectors, tag)
		}
		observatory["subjectSelector"] = selectors
		if observatory["probeURL"] == nil {
			observatory["probeURL"] = r.HealthURL
		}
		if observatory["probeInterval"] == nil {
			observatory["probeInterval"] = "10s"
		}
		observatory["enableConcurrency"] = true
		cfg["observatory"] = observatory
		routing["balancers"] = balancers
	}
	if policy.DefaultPreset != "" {
		routing["domainStrategy"] = "IPOnDemand"
		delete(routing, "script")
	} else if routing["domainStrategy"] == nil {
		routing["domainStrategy"] = "IPIfNonMatch"
	}
	routing["rules"] = append(generated, previous...)
	cfg["inbounds"], cfg["outbounds"], cfg["routing"] = inbounds, outbounds, routing
	return json.MarshalIndent(cfg, "", "  ")
}

func policyTag(action string, zapret bool) string {
	if action == "ZAPRET" && !zapret {
		action = "BLOCK"
	}
	return "manager-policy-" + strings.ToLower(action)
}

func policyProfileOutbound(raw string) (Object, error) {
	var root struct{ Outbounds []Object }
	if json.Unmarshal([]byte(raw), &root) != nil || len(root.Outbounds) != 1 {
		return nil, errors.New("для отдельного VPN-профиля нужен один outbound; для составного используйте тег активной конфигурации")
	}
	outbound := root.Outbounds[0]
	protocol, _ := outbound["protocol"].(string)
	if !vpnProtocol(protocol) {
		return nil, errors.New("выбранный профиль не содержит поддерживаемый VPN-outbound")
	}
	stream, _ := outbound["streamSettings"].(map[string]any)
	sockopt, _ := stream["sockopt"].(map[string]any)
	if outbound["proxySettings"] != nil || sockopt["dialerProxy"] != nil {
		return nil, errors.New("VPN-outbound зависит от другого тега")
	}
	return outbound, nil
}

func vpnProtocol(protocol string) bool {
	switch protocol {
	case "vless", "vmess", "hysteria", "socks", "trojan", "shadowsocks", "wireguard":
		return true
	default:
		return false
	}
}

func (r *Runtime) checkZapret(ctx context.Context) error {
	if !r.routing.UsesZapret {
		return nil
	}
	if r.Mark&(zapretMark|zapretGeneratedMark) != 0 {
		return errors.New("метка TProxy пересекается с зарезервированной меткой Zapret2")
	}
	return r.validateZapret(ctx, r.routing.Zapret, r.routing.Queue)
}

func (r *Runtime) ValidateZapret(ctx context.Context, settings api.ZapretSettings) error {
	return r.validateZapret(ctx, settings, 200)
}

func (r *Runtime) validateZapret(ctx context.Context, settings api.ZapretSettings, queue int) error {
	args := append([]string{"--dry-run", fmt.Sprintf("--qnum=%d", queue), fmt.Sprintf("--fwmark=0x%x", zapretGeneratedMark)}, settings.Args...)
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probe, settings.Binary, args...).CombinedOutput()
	if err != nil {
		fmt.Fprintln(&r.logs, string(output))
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		if detail := strings.TrimSpace(string(output)); detail != "" {
			return fmt.Errorf("nfqws2 не прошёл проверку параметров: %s (%w)", detail, err)
		}
		return fmt.Errorf("nfqws2 не прошёл проверку параметров: %w", err)
	}
	return nil
}

func (r *Runtime) ZapretRunning() bool {
	p := r.status.Load().proc
	return p != nil && p.running() && p.dpi.running()
}

func (r *Runtime) ProbeZapret(ctx context.Context, target string) (int, int, error) {
	if !r.ZapretRunning() {
		return 0, 0, errors.New("для проверки нужен запущенный Xray и активный пресет с ZAPRET")
	}
	client := socksClient(r.policyPort(16), 5*time.Second)
	for attempt := 1; ; attempt++ {
		status, latency, phase, err := probeZapretHTTPS(ctx, client, target)
		if err == nil {
			return status, latency, nil
		}
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		var networkErr net.Error
		switch {
		case errors.Is(err, syscall.ECONNRESET):
			err = errors.New("соединение сброшено")
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			err = errors.New("соединение закрыто до ответа")
		case errors.As(err, &networkErr) && networkErr.Timeout():
			err = errors.New("истекло время ожидания")
		default:
			return 0, 0, fmt.Errorf("%s: %w", phase, err)
		}
		if attempt == 2 {
			return 0, 0, fmt.Errorf("%s: %w после двух попыток", phase, err)
		}
	}
}

func probeZapretHTTPS(ctx context.Context, client *http.Client, target string) (int, int, string, error) {
	var stage atomic.Int32
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { stage.Store(1) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				stage.Store(2)
			}
		},
	}
	ctx = httptrace.WithClientTrace(ctx, trace)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, 0, "HTTPS", err
	}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		phase := [...]string{"Подключение через Xray", "TLS-рукопожатие", "Ожидание HTTP-ответа"}
		return 0, 0, phase[stage.Load()], err
	}
	defer response.Body.Close()
	return response.StatusCode, int(time.Since(start).Milliseconds()), "", nil
}

func (r *Runtime) policyFirewall(rules *strings.Builder) {
	add := func(match, family string, port int) {
		tproxy := fmt.Sprintf("tproxy to :%d", port)
		if family == "ip" {
			tproxy = fmt.Sprintf("tproxy ip to 127.0.0.1:%d", port)
		}
		if family == "ip6" {
			tproxy = fmt.Sprintf("tproxy ip6 to [::1]:%d", port)
		}
		fmt.Fprintf(rules, " %s meta l4proto { tcp, udp } %s meta mark set %d accept\n %s meta l4proto { tcp, udp } drop\n", match, tproxy, r.Mark, match)
	}
	// MAC matches take precedence over IP assignments.
	for i, preset := range r.routing.Presets {
		for _, value := range preset.Macs {
			if mac, err := net.ParseMAC(value); err == nil && len(mac) == 6 {
				add("ether saddr "+mac.String(), "", r.policyPort(i))
			}
		}
	}
	for i, preset := range r.routing.Presets {
		for _, value := range preset.Ips {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				continue
			}
			family := "ip"
			if prefix.Addr().Is6() {
				family = "ip6"
			}
			add(family+" saddr "+prefix.Masked().String(), family, r.policyPort(i))
		}
	}
}

func (r *Runtime) zapretFirewall(rules *strings.Builder) {
	if !r.routing.UsesZapret {
		return
	}
	fmt.Fprintf(rules, " chain zapret_output {\n type filter hook output priority -140; policy accept;\n meta mark 0x%x ct mark set ct mark | 0x%x queue num %d\n }\n", zapretMark, zapretMark, r.routing.Queue)
	fmt.Fprintf(rules, " chain zapret_reply {\n type filter hook prerouting priority -140; policy accept;\n iifname != %q ct direction reply ct mark & 0x%x == 0x%x queue num %d\n }\n", r.LAN, zapretMark, zapretMark, r.routing.Queue)
}
