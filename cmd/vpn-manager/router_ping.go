package main

import (
	"context"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
)

var pingLoss = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)% packet loss`)
var pingRTT = regexp.MustCompile(`= [0-9.]+/([0-9.]+)/[0-9.]+`)

func (m *localSample) watchPing(ctx context.Context, rt *engine.Runtime) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		telemetry := rt.Telemetry()
		state, _, _ := rt.Snapshot()
		result := api.RouterIcmp{}
		if state == "running" {
			var addresses []string
			for _, out := range telemetry.Outbounds {
				if out.Address != "" {
					addresses = append(addresses, out.Address)
				}
			}
			switch {
			case runtime.GOOS != "linux":
				result.Error = "ICMP-проверка доступна на Linux / OpenWrt"
			case len(addresses) != 1:
				result.Error = "Для ICMP нужен один адрес сервера в активной конфигурации"
			default:
				result = checkPing(ctx, addresses[0])
			}
		}
		m.Lock()
		m.ping, m.pingApplied = result, telemetry.Applied
		m.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func checkPing(ctx context.Context, endpoint string) api.RouterIcmp {
	result := api.RouterIcmp{Address: endpoint, CheckedAt: timestamp(time.Now())}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		result.Error = "Не удалось выделить адрес сервера"
		return result
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		result.Error = "DNS не разрешил адрес сервера"
		return result
	}
	tool := "ping"
	if ips[0].IP.To4() == nil {
		if _, err := exec.LookPath("ping6"); err == nil {
			tool = "ping6"
		}
	}
	cmd := exec.CommandContext(ctx, tool, "-q", "-c", "3", "-W", "1", "-w", "4", ips[0].String())
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	output, _ := cmd.CombinedOutput()
	loss := pingLoss.FindSubmatch(output)
	if len(loss) == 0 {
		result.Error = "Нет результата ICMP-проверки"
		return result
	}
	result.Available = true
	value, _ := strconv.ParseFloat(string(loss[1]), 32)
	result.Loss = float32(value)
	if match := pingRTT.FindSubmatch(output); len(match) > 0 {
		value, _ := strconv.ParseFloat(string(match[1]), 32)
		result.Rtt = float32(value)
	}
	return result
}

func (m *localSample) pingStatus(applied time.Time) api.RouterIcmp {
	m.RLock()
	defer m.RUnlock()
	if m.pingApplied != applied {
		return api.RouterIcmp{}
	}
	return m.ping
}
