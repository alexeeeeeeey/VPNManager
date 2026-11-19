package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/frontend"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/store"
)

type config struct {
	Listen      string         `json:"listen"`
	DataDir     string         `json:"dataDir"`
	Username    string         `json:"username"`
	Password    string         `json:"password"`
	TLSCert     string         `json:"tlsCert,omitempty"`
	TLSKey      string         `json:"tlsKey,omitempty"`
	TLSDisabled bool           `json:"tlsDisabled,omitempty"`
	Engine      engine.Options `json:"engine"`
	Development bool           `json:"-"`
}

func defaults() config {
	return config{Listen: "127.0.0.1:9080", DataDir: "/etc/vpn-manager", Username: "admin", Engine: engine.Options{
		Binary: "xray", TempDir: "/tmp/vpn-manager", LAN: "br-lan", TProxy: true, TProxyPort: 10808, SocksPort: 10818, StatsPort: 10085, Mark: 1048576, Table: 200, HealthURL: "https://www.gstatic.com/generate_204",
	}}
}

func loadConfig(path string, dev bool) (config, error) {
	cfg := defaults()
	if dev {
		cfg.Development = true
		cfg.DataDir = "dev-data"
		cfg.Engine.TempDir = filepath.Join(cfg.DataDir, "tmp")
		cfg.Engine.TProxy = false
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &cfg); err != nil {
			return cfg, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		var secret [24]byte
		if _, err = rand.Read(secret[:]); err != nil {
			return cfg, err
		}
		cfg.Password = hex.EncodeToString(secret[:])
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return cfg, err
		}
		data, err = json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return cfg, err
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			return cfg, err
		}
		slog.Info("Конфигурация создана", "path", path)
	} else {
		return cfg, err
	}
	if cfg.Password == "" || cfg.Username == "" {
		return cfg, errors.New("укажите username и password")
	}
	return cfg, nil
}

func (c config) validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return err
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tlsCert и tlsKey задаются вместе")
	}
	e := c.Engine
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`).MatchString(e.LAN) || e.LAN == "." || e.LAN == ".." {
		return errors.New("некорректный интерфейс LAN")
	}
	if e.Mark < 1 || e.Mark > 2147483647 || e.Table < 1 || e.Table >= 253 {
		return errors.New("некорректная метка или таблица маршрутизации")
	}
	ports := map[int]bool{}
	for _, port := range []int{e.TProxyPort, e.SocksPort, e.StatsPort} {
		if port < 1 || port > 65535 || ports[port] {
			return errors.New("порты Xray должны быть разными и в диапазоне 1–65535")
		}
		ports[port] = true
	}
	if e.DNSPort < 0 || e.DNSPort > 65535 || ports[e.DNSPort] {
		return errors.New("некорректный или занятый dnsPort")
	}
	for _, mac := range e.BypassMACs {
		if value, err := net.ParseMAC(mac); err != nil || len(value) != 6 {
			return errors.New("некорректный MAC в bypassMacs")
		}
	}
	target, err := url.Parse(e.HealthURL)
	if err != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return errors.New("некорректный healthUrl")
	}
	return nil
}

func run() error {
	path := flag.String("config", "/etc/vpn-manager/config.json", "JSON configuration")
	dev := flag.Bool("dev", false, "Local development with no TProxy or authentication")
	version := flag.Bool("version", false, "Print version")
	managedUpdate := flag.Bool("managed-update", false, "Enable binary updates under a process supervisor")
	legacy := flag.String("import-legacy", "", "Import enabled profiles from the old manager directory and exit")
	flag.Parse()
	if *version {
		fmt.Println(buildVersion)
		return nil
	}
	cfg, err := loadConfig(*path, *dev)
	if err != nil {
		return err
	}
	if err = cfg.validate(); err != nil {
		return err
	}
	for _, dir := range []string{cfg.DataDir, cfg.Engine.TempDir} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	unlock, err := lockInstance(cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dbFile := filepath.Join(cfg.DataDir, "data.db")
	file, err := os.OpenFile(dbFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	db, err := store.Open(ctx, dbFile)
	if err != nil {
		return err
	}
	defer db.Close()
	runtime := engine.New(cfg.Engine)
	manager := &manager{db: db, queries: store.New(db), runtime: runtime, metrics: &metrics{}, dataDir: cfg.DataDir}
	if *managedUpdate && !cfg.Development {
		manager.panelConfigPath, manager.panelRestart = *path, cancel
	}
	defer func() {
		if manager.zapretLuaDir != "" {
			_ = os.RemoveAll(manager.zapretLuaDir)
		}
	}()
	if *managedUpdate && !cfg.Development && goruntime.GOOS == "linux" && (goruntime.GOARCH == "amd64" || goruntime.GOARCH == "arm64") {
		executable, err := os.Executable()
		if err == nil {
			executable, err = filepath.EvalSymlinks(executable)
		}
		if err == nil {
			manager.updater.executable = executable
			manager.updater.restart = cancel
		}
	}
	if *legacy != "" {
		count, err := manager.importLegacy(ctx, *legacy)
		if err == nil {
			slog.Info("Импорт из старого менеджера", "profiles", count)
		}
		return err
	}
	defer runtime.Stop()
	dns, err := manager.dnsSettings(ctx)
	if err != nil {
		return fmt.Errorf("загрузка настроек DNS: %w", err)
	}
	if err = manager.validateDNS(dns); err != nil {
		slog.Warn("Сохранённые настройки DNS требуют исправления", "error", err)
		dns = api.DNSSettings{}
	}
	if err := manager.startDNS(dns); err != nil {
		slog.Warn("Локальный DNS недоступен", "error", err)
		if fallbackErr := manager.startDNS(api.DNSSettings{}); fallbackErr != nil {
			return fallbackErr
		}
	}
	defer func() {
		if err := manager.dnsGateway.forwardDNS(context.Background(), 0); err != nil {
			slog.Warn("Не удалось отключить перенаправление DNS", "error", err)
		}
		manager.dnsGateway.listener.stop()
	}()
	assets, err := frontend.Assets()
	if err != nil {
		return err
	}
	webServer := &http.Server{
		Addr: cfg.Listen, Handler: server{manager}.router(cfg, assets),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	if !manager.xrayWanted(ctx) {
		runtime.Stop()
	} else {
		manager.restore(ctx)
	}
	go manager.watchUpdates(ctx)
	go runtime.Watch(ctx)
	go manager.metrics.collect(ctx, runtime, cfg.Engine.LAN)
	go manager.routerMetrics.collect(ctx, runtime, cfg.DataDir)
	go manager.routerMetrics.watchPing(ctx, runtime)
	done := make(chan error, 1)
	go func() {
		slog.Info("VPN Manager запущен", "listen", cfg.Listen)
		if cfg.TLSCert != "" && !cfg.TLSDisabled {
			done <- webServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		done <- webServer.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdown, finish := context.WithTimeout(context.Background(), 8*time.Second)
		defer finish()
		return webServer.Shutdown(shutdown)
	case err := <-done:
		return err
	}
}

var buildVersion = "dev"

func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("VPN Manager", "error", err)
		os.Exit(1)
	}
}
