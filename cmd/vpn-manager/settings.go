package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/store"
)

const dnsSettingsKey = "dns_settings"
const zapretSettingsKey = "zapret2_settings"

func (m *manager) readSetting(ctx context.Context, key string, value any) error {
	raw, err := m.queries.Setting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), value)
}

func (m *manager) writeSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return m.queries.SetSetting(ctx, store.SetSettingParams{Key: key, Value: string(raw)})
}

func (m *manager) dnsSettings(ctx context.Context) (api.DNSSettings, error) {
	var saved struct {
		api.DNSSettings
		LegacyServers []string `json:"servers"`
	}
	saved.ListenPort = m.runtime.DNSPort
	if _, err := m.queries.Setting(ctx, dnsSettingsKey); errors.Is(err, sql.ErrNoRows) && m.panelRestart != nil && saved.ListenPort == 0 {
		if _, err := dnsmasqFragment(); err == nil {
			enabled := true
			saved.Enabled = &enabled
			saved.ListenPort = panelDNSPort
		}
	}
	if err := m.readSetting(ctx, dnsSettingsKey, &saved); err != nil {
		return api.DNSSettings{}, err
	}
	cfg := saved.DNSSettings
	if cfg.Stages == nil {
		cfg.Stages = []api.DNSSettingsStages{api.Xray, api.Doh, api.Direct}
		for _, server := range saved.LegacyServers {
			if strings.HasPrefix(server, "https://") {
				cfg.Doh = append(cfg.Doh, server)
			} else {
				cfg.Direct = append(cfg.Direct, server)
			}
		}
		if len(cfg.Doh) == 0 {
			cfg.Doh = []string{"https://1.1.1.1/dns-query"}
		}
		if len(cfg.Direct) == 0 {
			cfg.Direct = []string{"1.1.1.1"}
		}
	}
	if cfg.Doh == nil {
		cfg.Doh = []string{}
	}
	if cfg.Direct == nil {
		cfg.Direct = []string{}
	}
	return cfg, nil
}

func (m *manager) validateDNS(cfg api.DNSSettings) error {
	if cfg.ListenPort < 0 || cfg.ListenPort > 65535 {
		return errors.New("укажите порт от 0 до 65535")
	}
	for _, other := range []int{m.runtime.SocksPort, m.runtime.StatsPort, m.runtime.TProxyPort} {
		if cfg.ListenPort != 0 && cfg.ListenPort == other {
			return errors.New("этот порт уже используется Xray")
		}
	}
	if len(cfg.Stages) == 0 || len(cfg.Stages) > 3 {
		return errors.New("выберите от одного до трёх этапов")
	}
	stages := make(map[api.DNSSettingsStages]bool)
	for _, stage := range cfg.Stages {
		if !stage.Valid() || stages[stage] {
			return errors.New("каждый этап может встречаться один раз")
		}
		stages[stage] = true
	}
	if managedDNS(cfg) && !stages[api.Doh] && !stages[api.Direct] {
		return errors.New("включите DoH или Direct, чтобы DNS работал без VPN-профиля")
	}
	if len(cfg.Doh) > 4 || len(cfg.Direct) > 4 {
		return errors.New("до четырёх серверов на этап")
	}
	if stages[api.Doh] && len(cfg.Doh) == 0 {
		return errors.New("укажите хотя бы один URL DoH")
	}
	if (stages[api.Direct] || stages[api.Doh]) && len(cfg.Direct) == 0 {
		return errors.New("укажите хотя бы один IP для Direct и разрешения имён DoH")
	}
	for i, endpoint := range cfg.Doh {
		if !validDoH(endpoint) {
			return fmt.Errorf("DoH, строка %d: нужен полный URL HTTPS", i+1)
		}
	}
	for i, endpoint := range cfg.Direct {
		addr, err := netip.ParseAddrPort(directAddress(endpoint))
		if err != nil || !addr.IsValid() || addr.Port() == 0 || addr.Addr().IsUnspecified() || addr.Addr().Zone() != "" {
			return fmt.Errorf("Direct, строка %d: ожидается IP или IP:порт", i+1)
		}
		if addr.Addr().Unmap().IsLoopback() && (int(addr.Port()) == cfg.ListenPort || loopbackPort(int(addr.Port())) == m.dnsGateway.upstream) {
			return fmt.Errorf("Direct, строка %d: резолвер указывает на собственный DNS-вход", i+1)
		}
		if managedDNS(cfg) && addr.Port() == 53 {
			local, _ := net.InterfaceAddrs()
			for _, address := range local {
				prefix, err := netip.ParsePrefix(address.String())
				if err == nil && prefix.Addr().Unmap() == addr.Addr().Unmap() {
					return fmt.Errorf("Direct, строка %d: укажите вышестоящий DNS, а не сам роутер", i+1)
				}
			}
		}
	}
	return nil
}

func validDoH(value string) bool {
	if len(value) > 512 || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Path == "" || strings.HasSuffix(u.Host, ":") {
		return false
	}
	if u.Port() == "" {
		return true
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535
}

func (s server) GetDNSSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.dnsSettings(r.Context())
	jsonResponse(w, cfg, err)
}

func (s server) PutDNSSettings(w http.ResponseWriter, r *http.Request) {
	var cfg api.DNSSettings
	if !decode(w, r, &cfg) {
		return
	}
	if cfg.Enabled != nil {
		cfg.ListenPort = 0
		if *cfg.Enabled {
			cfg.ListenPort = panelDNSPort
		}
	}
	if cfg.Doh == nil {
		cfg.Doh = []string{}
	}
	if cfg.Direct == nil {
		cfg.Direct = []string{}
	}
	for _, endpoints := range [][]string{cfg.Doh, cfg.Direct} {
		for i, value := range endpoints {
			endpoints[i] = strings.TrimSpace(value)
		}
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if err := s.validateDNS(cfg); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	old, err := s.dnsSettings(r.Context())
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = s.dnsGateway.configure(ctx, cfg); err == nil {
		err = s.writeSetting(ctx, dnsSettingsKey, cfg)
	}
	if err != nil {
		rollback, finish := context.WithTimeout(context.Background(), 10*time.Second)
		defer finish()
		err = errors.Join(err, s.dnsGateway.configure(rollback, old))
		jsonResponse(w, nil, err)
		return
	}
	jsonResponse(w, cfg, nil)
}

func (s server) GetZapretSettings(w http.ResponseWriter, r *http.Request) {
	state, err := s.loadZapret(r.Context())
	jsonResponse(w, state.ZapretSettings, err)
}

func (s server) PutZapretSettings(w http.ResponseWriter, r *http.Request) {
	var cfg api.ZapretSettings
	if !decode(w, r, &cfg) {
		return
	}
	cfg.Binary = strings.TrimSpace(cfg.Binary)
	if (cfg.Binary != "" && !path.IsAbs(cfg.Binary)) || len(cfg.Binary) > 4096 || strings.ContainsAny(cfg.Binary, "\x00\r\n") {
		jsonResponse(w, nil, errors.New("укажите абсолютный путь к бинарнику nfqws2 на Linux"))
		return
	}
	if len(cfg.Args) > 256 {
		jsonResponse(w, nil, errors.New("можно сохранить до 256 аргументов"))
		return
	}
	if cfg.Args == nil {
		cfg.Args = []string{}
	}
	for i, arg := range cfg.Args {
		if strings.TrimSpace(arg) == "" || len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
			jsonResponse(w, nil, fmt.Errorf("некорректный аргумент в строке %d", i+1))
			return
		}
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	old, err := s.loadZapret(r.Context())
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	next := old
	next.ZapretSettings = cfg
	jsonResponse(w, cfg, s.changeZapret(r.Context(), old, next))
}
