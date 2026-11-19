package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	Binary         string   `json:"binary"`
	HysteriaBinary string   `json:"hysteriaBinary,omitempty"`
	TempDir        string   `json:"tempDir"`
	LAN            string   `json:"lan"`
	TProxy         bool     `json:"tproxy"`
	TProxyPort     int      `json:"tproxyPort"`
	SocksPort      int      `json:"socksPort"`
	StatsPort      int      `json:"statsPort"`
	DNSPort        int      `json:"dnsPort,omitempty"`
	Mark           int      `json:"mark"`
	Table          int      `json:"table"`
	BypassMACs     []string `json:"bypassMacs"`
	BypassDomains  []string `json:"bypassDomains"`
	HealthURL      string   `json:"healthUrl"`
}

type Runtime struct {
	Options
	mu                               sync.Mutex
	proc                             *process
	current                          string
	state, message                   string
	restarts, failures, activeChecks int
	retryAt                          time.Time
	logs                             logBuffer
	probes                           chan struct{}
	status                           atomic.Pointer[runtimeStatus]
	resolverPort                     int
	routing, appliedRouting          RoutingPolicy
	routingChanged                   bool
	telemetry                        Telemetry
	samples                          []bool
}

type runtimeStatus struct {
	proc           *process
	state, message string
	restarts       int
	telemetry      Telemetry
}
type process struct {
	cmd       *exec.Cmd
	done      chan struct{}
	companion *process
	dpi       *process
}
type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func New(options Options) *Runtime {
	r := &Runtime{Options: options, state: "stopped", probes: make(chan struct{}, 4)}
	r.publish()
	return r
}

func (r *Runtime) publish() {
	r.status.Store(&runtimeStatus{r.proc, r.state, r.message, r.restarts, r.telemetry})
}

func (l *logBuffer) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	l.lines = append(l.lines, lines...)
	if len(l.lines) > 150 {
		l.lines = append([]string(nil), l.lines[len(l.lines)-150:]...)
	}
	return len(data), nil
}
func (r *Runtime) Logs() []string {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	return append([]string{}, r.logs.lines...)
}

func startProcess(writer io.Writer, binary string, args ...string) (*process, error) {
	cmd := exec.Command(binary, args...)
	tieProcessToManager(cmd)
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *process) alive() bool {
	return p.running() && (p.companion == nil || p.companion.running()) && (p.dpi == nil || p.dpi.running())
}

func (p *process) running() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *process) stop() {
	if p.running() {
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	if p != nil && p.companion != nil {
		p.companion.stop()
	}
	if p != nil && p.dpi != nil {
		p.dpi.stop()
	}
}

func waitReady(ctx context.Context, p *process, port int) error {
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return errors.New("процесс завершился при запуске")
		case <-deadline.C:
			return errors.New("процесс не открыл SOCKS-порт")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

// SetResolverPort configures the private DNS inbound before the runtime starts.
func (r *Runtime) SetResolverPort(port int) { r.resolverPort = port }

func (r *Runtime) writeConfig(raw string, socks, transparent, stats int) (string, error) {
	dns := 0
	var bypass []string
	if stats > 0 {
		dns, bypass = r.resolverPort, r.BypassDomains
	}
	data, err := build(raw, socks, transparent, stats, dns, bypass)
	if err != nil {
		return "", err
	}
	if transparent > 0 {
		data, err = r.buildRouting(data, r.routing)
		if err != nil {
			return "", err
		}
	}
	return writeTemp(r.TempDir, "xray-*.json", data)
}

func writeTemp(dir, name string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, name)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (r *Runtime) Apply(ctx context.Context, raw string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.publish()
	return r.apply(ctx, raw)
}

func (r *Runtime) apply(ctx context.Context, raw string) (result error) {
	defer func() {
		if result != nil {
			r.event("config", result.Error())
		}
	}()
	tproxy := 0
	if r.TProxy {
		tproxy = r.TProxyPort
	}
	config, companion, err := r.prepareSidecar(ctx, raw, &r.logs)
	if err != nil {
		return err
	}
	defer func() {
		if r.proc == nil || r.proc.companion != companion {
			companion.stop()
		}
	}()
	path, err := r.writeConfig(config, r.SocksPort, tproxy, r.StatsPort)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	validation, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(validation, r.Binary, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		fmt.Fprintf(&r.logs, "Проверка Xray: %v\n%s", err, output)
		return errors.New("Xray отклонил конфигурацию. Подробности в журнале")
	}
	if r.TProxy {
		if err = r.checkZapret(ctx); err != nil {
			return err
		}
		if err = r.checkFirewall(ctx); err != nil {
			r.event("tproxy", err.Error())
			return err
		}
	}
	old := r.current
	r.proc.stop()
	r.proc = nil
	r.state, r.message = "starting", ""
	r.publish()
	if err = r.start(ctx, path, companion); err == nil {
		r.appliedRouting = r.routing
		now := time.Now()
		if old == raw {
			r.telemetry.Reconnected = now
			r.telemetry.Reconnects++
		}
		if old != raw {
			r.samples = nil
			r.telemetry.Health = Health{}
		}
		r.telemetry.Applied = now
		r.telemetry.Outbounds, _ = configTelemetry(raw)
		if generated, readErr := os.ReadFile(path); readErr == nil {
			actual, rules := configTelemetry(string(generated))
			for i := 0; i < min(len(actual), len(r.telemetry.Outbounds)); i++ {
				r.telemetry.Outbounds[i].Tag = actual[i].Tag
			}
			for i := len(r.telemetry.Outbounds); i < len(actual); i++ {
				r.telemetry.Outbounds = append(r.telemetry.Outbounds, actual[i])
			}
			r.telemetry.Rules = rules
		}
		r.current = raw
		r.state, r.message = "running", ""
		r.failures, r.activeChecks = 0, 0
		r.retryAt = time.Time{}
		return nil
	}
	r.state, r.message = "error", err.Error()
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelCleanup()
	policy := r.routing
	defer func() { r.routing = policy }()
	if old != "" && (old != raw || r.routingChanged) {
		r.routing = r.appliedRouting
		oldConfig, oldCompanion, buildErr := r.prepareSidecar(cleanup, old, &r.logs)
		if buildErr == nil {
			previous, buildErr := r.writeConfig(oldConfig, r.SocksPort, tproxy, r.StatsPort)
			if buildErr == nil {
				defer os.Remove(previous)
				if r.start(cleanup, previous, oldCompanion) == nil {
					r.state = "running"
					return err
				}
			}
			oldCompanion.stop()
		}
	}
	if r.TProxy {
		r.clearFirewall(cleanup)
	}
	return err
}

func (r *Runtime) start(ctx context.Context, path string, companion *process) error {
	var dpi *process
	if r.TProxy && r.routing.UsesZapret {
		args := append([]string{fmt.Sprintf("--qnum=%d", r.routing.Queue), fmt.Sprintf("--fwmark=0x%x", zapretGeneratedMark)}, r.routing.Zapret.Args...)
		var err error
		dpi, err = startProcess(&r.logs, r.routing.Zapret.Binary, args...)
		if err != nil {
			return fmt.Errorf("запуск nfqws2: %w", err)
		}
	}
	p, err := startProcess(&r.logs, r.Binary, "run", "-config", path)
	if err != nil {
		dpi.stop()
		return fmt.Errorf("запуск Xray: %w", err)
	}
	p.companion = companion
	p.dpi = dpi
	if dpi != nil {
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			p.stop()
			return ctx.Err()
		case <-dpi.done:
			p.stop()
			return errors.New("nfqws2 завершился при запуске")
		case <-timer.C:
		}
	}
	if err = waitReady(ctx, p, r.SocksPort); err == nil && r.TProxy {
		err = r.ensureFirewall(ctx)
		if err != nil {
			r.event("tproxy", err.Error())
		}
	}
	if err != nil {
		p.stop()
		return err
	}
	r.proc = p
	return nil
}

func (r *Runtime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.publish()
	r.proc.stop()
	r.proc = nil
	r.current = ""
	r.telemetry.Health = Health{}
	r.samples = nil
	r.telemetry.Outbounds = nil
	r.telemetry.Rules = nil
	r.state, r.message = "stopped", ""
	if r.TProxy {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.clearFirewall(ctx)
	}
}

func (r *Runtime) Snapshot() (string, string, int) {
	status := r.status.Load()
	if status.state == "running" && !status.proc.alive() {
		return "error", "Процесс VPN завершился", status.restarts
	}
	return status.state, status.message, status.restarts
}

func socksClient(port int, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", port)}), DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (r *Runtime) verify(ctx context.Context, port int) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.HealthURL, nil)
	if err != nil {
		return err
	}
	response, err := socksClient(port, 5*time.Second).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func (r *Runtime) Watch(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.watch(ctx)
		}
	}
}

func (r *Runtime) watch(ctx context.Context) {
	r.mu.Lock()
	current, proc := r.current, r.proc
	if current == "" {
		r.mu.Unlock()
		return
	}
	if proc != nil && !proc.alive() {
		r.telemetry.Crashes++
		r.event("crash", "Процесс VPN неожиданно завершился")
		proc.stop()
		r.proc, proc = nil, nil
		r.state, r.message = "error", "Процесс VPN завершился"
		r.publish()
		if r.TProxy {
			cleanup, cancel := context.WithTimeout(ctx, 3*time.Second)
			r.clearFirewall(cleanup)
			cancel()
		}
	}
	if time.Now().Before(r.retryAt) {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	started := time.Now()
	var checkErr error
	if proc.alive() {
		checkErr = r.verify(ctx, r.SocksPort)
	} else {
		checkErr = errors.New("процесс VPN недоступен")
	}
	elapsed := int(time.Since(started).Milliseconds())
	broken := checkErr != nil
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.publish()
	if current != r.current || proc != r.proc || ctx.Err() != nil {
		return
	}
	r.recordHealth(time.Now(), elapsed, checkErr)
	if broken && r.activeChecks == 0 {
		r.event("connection", checkErr.Error())
	}
	if !broken {
		r.activeChecks, r.failures = 0, 0
		return
	}
	if proc.alive() && len(r.appliedRouting.Presets) > 0 {
		// Observatory handles VPN failures without restarting the shared router.
		if r.activeChecks == 0 {
			r.activeChecks = 1
		}
		return
	}
	r.activeChecks++
	if proc.alive() && r.activeChecks < 3 {
		return
	}
	r.restarts++
	failures := r.failures + 1
	attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := r.apply(attempt, current)
	r.failures = failures
	r.retryAt = time.Now().Add(time.Duration(min(failures, 8)) * 15 * time.Second)
	if err != nil {
		r.state, r.message = "error", err.Error()
	} else {
		r.message = "Xray автоматически перезапущен"
	}
}

func (r *Runtime) Probe(ctx context.Context, raw string) (int, error) {
	select {
	case r.probes <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-r.probes }()
	config, companion, err := r.prepareSidecar(ctx, raw, io.Discard)
	if err != nil {
		return 0, err
	}
	defer companion.stop()
	port, err := freeTCPPort()
	if err != nil {
		return 0, err
	}
	path, err := r.writeConfig(config, port, 0, 0)
	if err != nil {
		return 0, err
	}
	defer os.Remove(path)
	p, err := startProcess(io.Discard, r.Binary, "run", "-config", path)
	if err != nil {
		return 0, err
	}
	defer p.stop()
	if err = waitReady(ctx, p, port); err != nil {
		return 0, err
	}
	start := time.Now()
	if err = r.verify(ctx, port); err != nil {
		return 0, errors.New("нет ответа через профиль")
	}
	return int(time.Since(start).Milliseconds()), nil
}

type Traffic struct{ Rx, Tx int }

func (r *Runtime) Stats(ctx context.Context) (inbound Traffic, outbound map[string]Traffic) {
	if r.StatsPort == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, r.Binary, "api", "statsquery", "--server="+fmt.Sprintf("127.0.0.1:%d", r.StatsPort)).Output()
	if err != nil {
		return
	}
	var values struct {
		Stat []struct {
			Name  string
			Value json.RawMessage
		}
	}
	if json.Unmarshal(output, &values) != nil {
		return
	}
	outbound = make(map[string]Traffic)
	for _, metric := range values.Stat {
		value, _ := strconv.Atoi(strings.Trim(string(metric.Value), "\""))
		parts := strings.Split(metric.Name, ">>>")
		if len(parts) != 4 || parts[2] != "traffic" {
			continue
		}
		switch parts[0] {
		case "inbound":
			if parts[1] != "manager-socks" && parts[1] != "manager-tproxy" && !strings.HasPrefix(parts[1], "manager-policy-in-") {
				continue
			}
			if parts[3] == "downlink" {
				inbound.Rx += value
			} else if parts[3] == "uplink" {
				inbound.Tx += value
			}
		case "outbound":
			current := outbound[parts[1]]
			if parts[3] == "downlink" {
				current.Rx = value
			} else if parts[3] == "uplink" {
				current.Tx = value
			}
			outbound[parts[1]] = current
		}
	}
	return
}

func (r *Runtime) firewall() string {
	var rules strings.Builder
	fmt.Fprintf(&rules, "table inet vpn_manager {\n chain prerouting {\n type filter hook prerouting priority -150; policy accept;\n iifname != %q return\n", r.LAN)
	rules.WriteString(" fib daddr type { local, broadcast, multicast } return\n ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4 } return\n ip6 daddr { ::/128, ::1/128, fc00::/7, fe80::/10, ff00::/8 } return\n")
	r.policyFirewall(&rules)
	for _, raw := range r.BypassMACs {
		mac, err := net.ParseMAC(raw)
		if err == nil && len(mac) == 6 {
			fmt.Fprintf(&rules, " ether saddr %s return\n", mac)
		}
	}
	fmt.Fprintf(&rules, " meta l4proto { tcp, udp } tproxy to :%d meta mark set %d accept\n }\n", r.TProxyPort, r.Mark)
	r.zapretFirewall(&rules)
	fmt.Fprintf(&rules, " counter forwarded_tx {}\n counter forwarded_rx {}\n chain forwarded {\n type filter hook forward priority 10; policy accept;\n iifname %q oifname != %q counter name forwarded_tx\n oifname %q iifname != %q counter name forwarded_rx\n }\n}\n", r.LAN, r.LAN, r.LAN, r.LAN)
	return rules.String()
}

func command(ctx context.Context, stdin string, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w %s", argv[0], err, bytes.TrimSpace(output))
	}
	return nil
}

func (r *Runtime) nft(ctx context.Context, check bool) error {
	rules := r.firewall()
	if command(ctx, "", "nft", "list", "table", "inet", "vpn_manager") == nil {
		rules = "delete table inet vpn_manager\n" + rules
	}
	args := []string{"nft", "-f", "-"}
	if check {
		args = append(args, "-c")
	}
	return command(ctx, rules, args...)
}

func (r *Runtime) checkFirewall(ctx context.Context) error { return r.nft(ctx, true) }

func (r *Runtime) ensureFirewall(ctx context.Context) error {
	table, mark := strconv.Itoa(r.Table), strconv.Itoa(r.Mark)
	for _, family := range []string{"-4", "-6"} {
		prefix := "0.0.0.0/0"
		if family == "-6" {
			prefix = "::/0"
		}
		if err := command(ctx, "", "ip", family, "route", "replace", "local", prefix, "dev", "lo", "table", table); err != nil {
			return err
		}
		output, err := exec.CommandContext(ctx, "ip", family, "rule", "show").Output()
		if err != nil {
			return err
		}
		ruleExists := strings.Contains(string(output), fmt.Sprintf("fwmark 0x%x lookup %s", r.Mark, table))
		if !ruleExists {
			if err := command(ctx, "", "ip", family, "rule", "add", "priority", "12000", "fwmark", mark, "lookup", table); err != nil {
				return err
			}
		}
	}
	return r.nft(ctx, false)
}

func (r *Runtime) clearFirewall(ctx context.Context) {
	_ = command(ctx, "", "nft", "delete", "table", "inet", "vpn_manager")
	for _, family := range []string{"-4", "-6"} {
		_ = command(ctx, "", "ip", family, "rule", "del", "priority", "12000", "fwmark", strconv.Itoa(r.Mark), "lookup", strconv.Itoa(r.Table))
		prefix := "0.0.0.0/0"
		if family == "-6" {
			prefix = "::/0"
		}
		_ = command(ctx, "", "ip", family, "route", "del", "local", prefix, "dev", "lo", "table", strconv.Itoa(r.Table))
	}
}

func (r *Runtime) Restore(ctx context.Context, raw string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.publish()
	if err := r.apply(ctx, raw); err != nil {
		r.current = raw
		r.state, r.message = "error", err.Error()
		if r.TProxy {
			cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			r.clearFirewall(cleanup)
		}
	}
}
