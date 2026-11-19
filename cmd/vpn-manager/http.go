package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-playground/validator/v10"
)

type server struct{ *manager }

var validate = validator.New()

func jsonResponse(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errMissing) || errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
			err = errMissing
		}
		if errors.Is(err, errActive) {
			status = http.StatusConflict
		}
		w.WriteHeader(status)
		value = api.Error{Message: err.Error()}
	}
	_ = json.NewEncoder(w).Encode(value)
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxInput*2)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil || decoder.Decode(new(any)) != io.EOF {
		jsonResponse(w, nil, errors.New("некорректный запрос или превышен лимит размера"))
		return false
	}
	return true
}

func (s server) GetStatus(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, s.status(r.Context()), nil)
}
func (s server) GetInventory(w http.ResponseWriter, r *http.Request) { s.returnInventory(w, r, nil) }
func (s server) returnInventory(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	data, err := s.inventory(r.Context())
	jsonResponse(w, data, err)
}
func (s server) ImportProfiles(w http.ResponseWriter, r *http.Request) {
	var input api.Input
	if !decode(w, r, &input) {
		return
	}
	if validate.Var(input.Input, "required,max=4194304") != nil || validate.Var(input.Name, "max=100") != nil || validate.Var(input.RefreshHours, "gte=0,lte=720") != nil {
		jsonResponse(w, nil, errors.New("проверьте содержимое, имя и интервал обновления"))
		return
	}
	s.returnInventory(w, r, s.importInput(r.Context(), input))
}
func (s server) RefreshSubscription(w http.ResponseWriter, r *http.Request, id string) {
	s.returnInventory(w, r, s.refresh(r.Context(), id))
}
func (s server) DeleteSubscription(w http.ResponseWriter, r *http.Request, id string) {
	s.returnInventory(w, r, s.remove(r.Context(), id, true))
}
func (s server) DeleteProfile(w http.ResponseWriter, r *http.Request, id string) {
	s.returnInventory(w, r, s.remove(r.Context(), id, false))
}
func (s server) GetConfig(w http.ResponseWriter, r *http.Request, id string) {
	p, err := s.queries.ProfileByID(r.Context(), id)
	jsonResponse(w, api.Config{Input: p.Config}, err)
}
func (s server) UpdateConfig(w http.ResponseWriter, r *http.Request, id string) {
	var input api.Config
	if !decode(w, r, &input) {
		return
	}
	if len(input.Input) > maxInput {
		jsonResponse(w, nil, errors.New("конфигурация больше 4 МБ"))
		return
	}
	s.returnInventory(w, r, s.edit(r.Context(), id, input.Input))
}
func (s server) ActivateProfile(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.activate(r.Context(), id); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	s.GetStatus(w, r)
}
func (s server) StopXray(w http.ResponseWriter, r *http.Request) {
	if err := s.stop(r.Context()); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	s.GetStatus(w, r)
}
func (s server) RestartXray(w http.ResponseWriter, r *http.Request) {
	if err := s.restart(r.Context()); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	s.GetStatus(w, r)
}
func (s server) ClearXrayProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.clearXrayProfile(r.Context()); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	s.GetStatus(w, r)
}
func (s server) ProbeProfiles(w http.ResponseWriter, r *http.Request) {
	var body api.ProbeRequest
	if !decode(w, r, &body) {
		return
	}
	if len(body.Ids) == 0 || len(body.Ids) > 16 {
		jsonResponse(w, nil, errors.New("ожидается от 1 до 16 идентификаторов"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	jsonResponse(w, s.probes(ctx, body.Ids), nil)
}
func (s server) GetLogs(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, api.Logs{Lines: s.runtime.Logs()}, nil)
}

func (s server) status(ctx context.Context) api.Status {
	status := s.metrics.snapshot()
	status.ActiveId = s.active(ctx)
	status.XrayEnabled = s.xrayWanted(ctx)
	status.Xray, status.Message, status.Restarts = s.runtime.Snapshot()
	status.Tproxy = s.runtime.TProxy && status.Xray == "running"
	return status
}

func (s server) live(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	sessionValid, _ := r.Context().Value(sessionCheckKey{}).(func() bool)
	for {
		if sessionValid != nil && !sessionValid() {
			_ = conn.Close(websocket.StatusPolicyViolation, "session expired")
			return
		}
		send, cancel := context.WithTimeout(ctx, time.Second)
		err = wsjson.Write(send, conn, s.status(send))
		cancel()
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s server) router(cfg config, assets fs.FS) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "same-origin")
			w.Header().Set("X-Frame-Options", "DENY")
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Cache-Control", "no-store")
				if r.Method != http.MethodGet && !sameOrigin(r) {
					http.Error(w, "Cross-site request", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Use(authentication(cfg))
	r.Get("/api/live", s.live)
	s.panelSettingsRoutes(r, cfg)
	s.autoUpdateRoutes(r)
	r.Group(func(group chi.Router) {
		group.Use(middleware.Compress(5, "text/html", "text/css", "application/javascript", "text/javascript", "application/json", "image/svg+xml"))
		api.HandlerFromMux(s, group)
		group.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" || !strings.Contains(name, ".") {
				data, err := fs.ReadFile(assets, "index.html")
				if err != nil {
					http.Error(w, "Build the frontend with pnpm --dir frontend build", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				_, _ = w.Write(data)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.FileServer(http.FS(assets)).ServeHTTP(w, r)
		})
	})
	return r
}

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
}

type metrics struct {
	mu           sync.RWMutex
	data         api.Status
	total, idle  uint64
	outbounds    map[string]engine.Traffic
	statsApplied time.Time
}

func (m *metrics) snapshot() api.Status {
	value, _, _ := m.sample()
	return value
}

func (m *metrics) sample() (api.Status, map[string]engine.Traffic, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data, m.outbounds, m.statsApplied
}

func number(value string) int {
	n, _ := strconv.ParseFloat(value, 64)
	return int(n)
}

func (m *metrics) collect(ctx context.Context, rt *engine.Runtime, lan string) {
	host, _ := os.Hostname()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		result := api.Status{Hostname: host}
		sampledAt := time.Now().UTC().Format(time.RFC3339Nano)
		result.SampledAt = &sampledAt
		if raw, _ := os.ReadFile("/proc/uptime"); len(raw) > 0 {
			if fields := strings.Fields(string(raw)); len(fields) > 0 {
				result.Uptime = number(fields[0])
			}
		}
		if raw, _ := os.ReadFile("/proc/stat"); len(raw) > 0 {
			row, _, _ := strings.Cut(string(raw), "\n")
			parts := strings.Fields(row)
			if len(parts) >= 6 {
				var total uint64
				for _, part := range parts[1:min(len(parts), 9)] {
					n, _ := strconv.ParseUint(part, 10, 64)
					total += n
				}
				idle := uint64(number(parts[4]) + number(parts[5]))
				if m.total > 0 && total > m.total && idle >= m.idle && idle-m.idle <= total-m.total {
					result.Cpu = int(100 * (total - m.total - idle + m.idle) / (total - m.total))
				}
				m.total, m.idle = total, idle
			}
		}
		if raw, _ := os.ReadFile("/proc/meminfo"); len(raw) > 0 {
			available := 0
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				switch fields[0] {
				case "MemTotal:":
					result.MemoryTotal = number(fields[1]) * 1024
				case "MemAvailable:":
					available = number(fields[1]) * 1024
				}
			}
			result.MemoryUsed = max(0, result.MemoryTotal-available)
		}
		result.RxBytes, result.TxBytes = lanCounters(lan)
		if total, idle, used, memory := platformUsage(); total > 0 {
			if m.total > 0 && total > m.total && idle >= m.idle && idle-m.idle <= total-m.total {
				result.Cpu = int(100 * (total - m.total - idle + m.idle) / (total - m.total))
			}
			m.total, m.idle = total, idle
			result.MemoryUsed, result.MemoryTotal = used, memory
		}
		var outbounds map[string]engine.Traffic
		applied := rt.Telemetry().Applied
		if state, _, _ := rt.Snapshot(); state == "running" {
			inbound, stats := rt.Stats(ctx)
			result.XrayRxBytes, result.XrayTxBytes = inbound.Rx, inbound.Tx
			outbounds = stats
		}
		m.mu.Lock()
		m.data = result
		m.outbounds, m.statsApplied = outbounds, applied
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func lanCounters(lan string) (int, int) {
	rx, rxErr := os.ReadFile("/sys/class/net/" + lan + "/statistics/rx_bytes")
	tx, txErr := os.ReadFile("/sys/class/net/" + lan + "/statistics/tx_bytes")
	if rxErr == nil && txErr == nil {
		return number(strings.TrimSpace(string(rx))), number(strings.TrimSpace(string(tx)))
	}
	data, _ := os.ReadFile("/proc/net/dev")
	for _, line := range strings.Split(string(data), "\n") {
		name, counters, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != lan {
			continue
		}
		values := strings.Fields(counters)
		if len(values) > 8 {
			return number(values[0]), number(values[8])
		}
	}
	return 0, 0
}

func (m *manager) restore(ctx context.Context) {
	raw, err := m.xrayConfig(ctx, m.active(ctx))
	if err != nil {
		slog.Error("Загрузка профиля Xray", "error", err)
		return
	}
	saved, err := m.routingSettings(ctx)
	if err != nil {
		slog.Error("Загрузка пресетов", "error", err)
		return
	}
	policy, err := m.prepareRouting(ctx, &saved)
	if err != nil {
		slog.Error("Проверка пресетов", "error", err)
		return
	}
	policy.DirectOnly = raw == directXrayConfig
	m.runtime.SetRouting(policy)
	m.runtime.Restore(ctx, raw)
}

var _ api.ServerInterface = server{}
