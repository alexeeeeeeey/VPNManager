package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
)

const zapretTestMark = 0x10000000
const zapretTestTable = "vpn_manager_zapret_test"

func (r *Runtime) RunZapretTrials(ctx context.Context, targets []string, trials []api.ZapretTrial, binaries []api.ZapretSettings, activeQueue int, progress func(int)) error {
	if r.Mark&zapretTestMark != 0 {
		return errors.New("метка TProxy пересекается с меткой проверки Zapret2")
	}
	queue := activeQueue
	for n := 60000; n <= 60015; n++ {
		if n != activeQueue && !queueAttached(n) {
			queue = n
			break
		}
	}
	if queue == activeQueue {
		return errors.New("нет свободной тестовой очереди NFQUEUE")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	raw, err := json.Marshal(Object{
		"log":       Object{"loglevel": "warning"},
		"inbounds":  []Object{{"listen": "127.0.0.1", "port": port, "protocol": "socks"}},
		"outbounds": []Object{{"protocol": "freedom", "streamSettings": Object{"sockopt": Object{"mark": zapretTestMark}}}},
	})
	if err != nil {
		return err
	}
	path, err := writeTemp(r.TempDir, "xray-zapret-test-*.json", raw)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	xray, err := startProcess(&r.logs, r.Binary, "run", "-config", path)
	if err != nil {
		return err
	}
	defer xray.stop()
	if err := waitReady(ctx, xray, port); err != nil {
		return err
	}
	clearRules := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = command(cleanup, "", "nft", "delete", "table", "inet", zapretTestTable)
	}
	clearRules()
	probeZapretBatch(ctx, port, targets, &trials[0], func() { progress(0) })
	if err := ctx.Err(); err != nil {
		return err
	}
	rules := fmt.Sprintf(`table inet %s {
 chain output {
  type filter hook output priority -140; policy accept;
  meta mark 0x%x ct mark set ct mark | 0x%x queue num %d
 }
 chain reply {
  type filter hook prerouting priority -140; policy accept;
  ct direction reply ct mark & 0x%x == 0x%x queue num %d
 }
}`, zapretTestTable, zapretTestMark, zapretTestMark, queue, zapretTestMark, zapretTestMark, queue)
	if err := command(ctx, rules, "nft", "-f", "-"); err != nil {
		return fmt.Errorf("не удалось подключить тестовую NFQUEUE: %w", err)
	}
	defer clearRules()
	for i, settings := range binaries {
		if err := ctx.Err(); err != nil {
			return err
		}
		result := &trials[i+1]
		progress(i + 1)
		if err := r.validateZapret(ctx, settings, queue); err != nil {
			result.Error = err.Error()
			progress(i + 1)
			continue
		}
		args := append([]string{fmt.Sprintf("--qnum=%d", queue), fmt.Sprintf("--fwmark=0x%x", zapretGeneratedMark)}, settings.Args...)
		worker, err := startProcess(&r.logs, settings.Binary, args...)
		if err != nil {
			result.Error = err.Error()
			progress(i + 1)
			continue
		}
		if waitZapretQueue(ctx, worker, queue) {
			probeZapretBatch(ctx, port, targets, result, func() { progress(i + 1) })
		} else {
			result.Error = "nfqws2 не подключился к тестовой очереди"
			progress(i + 1)
		}
		worker.stop()
	}
	return ctx.Err()
}

func queueAttached(queue int) bool {
	data, err := os.ReadFile("/proc/net/netfilter/nfnetlink_queue")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == strconv.Itoa(queue) {
			return true
		}
	}
	return false
}

func waitZapretQueue(ctx context.Context, worker *process, queue int) bool {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		if queueAttached(queue) {
			return true
		}
		select {
		case <-worker.done:
			return false
		case <-ctx.Done():
			return false
		case <-timeout.C:
			return false
		case <-ticker.C:
		}
	}
}

func probeZapretBatch(ctx context.Context, port int, targets []string, trial *api.ZapretTrial, progress func()) {
	type response struct{ index, status int }
	results := make(chan response, len(targets))
	trial.Statuses = make([]int, len(targets))
	for i := range trial.Statuses {
		trial.Statuses[i] = -1
	}
	progress()
	client := socksClient(port, 3*time.Second)
	for i, target := range targets {
		go func() {
			status, _, _, _ := probeZapretHTTPS(ctx, client, target)
			results <- response{i, status}
		}()
	}
	for range targets {
		response := <-results
		trial.Statuses[response.index] = response.status
		progress()
	}
}
