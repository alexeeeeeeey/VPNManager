package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
)

const flowLimit = 20000

type flow struct {
	key, source, destination, protocol string
	rx, tx                             int
	accounted                          bool
}

type localSample struct {
	probeFailures map[string]api.RouterEvent
	sync.RWMutex
	dashboard   api.RouterDashboard
	flows       map[string]flow
	totals      map[string]engine.Traffic
	at          time.Time
	ping        api.RouterIcmp
	pingApplied time.Time
}

func (m *localSample) recordProbe(id, name string, err error) {
	m.Lock()
	defer m.Unlock()
	if err == nil {
		delete(m.probeFailures, id)
		return
	}
	if m.probeFailures == nil {
		m.probeFailures = make(map[string]api.RouterEvent)
	}
	if len(m.probeFailures) >= 32 {
		var oldest string
		for key, value := range m.probeFailures {
			if oldest == "" || value.At < m.probeFailures[oldest].At {
				oldest = key
			}
		}
		delete(m.probeFailures, oldest)
	}
	m.probeFailures[id] = api.RouterEvent{At: timestamp(time.Now()), Kind: "probe", Message: name + " · " + err.Error()}
}

func (m *localSample) failedProbes() []api.RouterEvent {
	m.RLock()
	defer m.RUnlock()
	oldest := timestamp(time.Now().Add(-15 * time.Minute))
	var results []api.RouterEvent
	for _, value := range m.probeFailures {
		if value.At >= oldest {
			results = append(results, value)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].At < results[j].At })
	return results
}

func parseFlow(line string) (f flow, ok bool) {
	fields := strings.Fields(line)
	protocolAt := -1
	for i, v := range fields[:min(len(fields), 5)] {
		if v == "tcp" || v == "udp" {
			protocolAt = i
			break
		}
	}
	if protocolAt < 0 {
		return f, false
	}
	f.protocol = fields[protocolAt]
	var srcPort, dstPort string
	tuple, counts := 0, 0
	for _, field := range fields[protocolAt+1:] {
		key, val, _ := strings.Cut(field, "=")
		switch key {
		case "src":
			tuple++
			if tuple == 1 {
				f.source = val
			}
		case "dst":
			if tuple == 1 {
				f.destination = val
			}
		case "sport":
			if tuple == 1 {
				srcPort = val
			}
		case "dport":
			if tuple == 1 {
				dstPort = val
			}
		case "bytes":
			value, err := strconv.Atoi(val)
			if err != nil || value < 0 {
				continue
			}
			if tuple == 1 {
				f.tx = value
				counts |= 1
			}
			if tuple == 2 {
				f.rx = value
				counts |= 2
			}
		}
	}
	src, srcErr := netip.ParseAddr(f.source)
	dst, dstErr := netip.ParseAddr(f.destination)
	if srcErr != nil || dstErr != nil || srcPort == "" || dstPort == "" {
		return f, false
	}
	f.source = src.String()
	f.destination = net.JoinHostPort(dst.String(), dstPort)
	f.key = f.protocol + " " + net.JoinHostPort(f.source, srcPort) + " " + f.destination
	f.accounted = counts == 3
	return f, true
}

func scanFlows(reader io.Reader) (map[string]flow, bool, error) {
	result := make(map[string]flow)
	scanner := bufio.NewScanner(reader)
	for count := 0; scanner.Scan(); count++ {
		if count >= flowLimit {
			return result, true, nil
		}
		if f, ok := parseFlow(scanner.Text()); ok {
			result[f.key] = f
		}
	}
	return result, false, scanner.Err()
}

func readFlows(ctx context.Context) (map[string]flow, bool, error) {
	if runtime.GOOS != "linux" {
		return nil, false, errors.New("conntrack доступен на Linux")
	}
	for _, path := range []string{"/proc/net/nf_conntrack", "/proc/net/ip_conntrack"} {
		file, err := os.Open(path)
		if err == nil {
			result, truncated, scanErr := scanFlows(file)
			file.Close()
			return result, truncated, scanErr
		}
	}
	cmd := exec.CommandContext(ctx, "conntrack", "-L", "-o", "extended")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err = cmd.Start(); err != nil {
		return nil, false, err
	}
	result, truncated, scanErr := scanFlows(pipe)
	if truncated {
		_ = cmd.Process.Kill()
	}
	pipe.Close()
	waitErr := cmd.Wait()
	if truncated {
		waitErr = nil
	}
	return result, truncated, errors.Join(scanErr, waitErr)
}

func readNeighbors(ctx context.Context, lan string) (map[string]string, map[string]string, bool) {
	if runtime.GOOS != "linux" {
		return nil, nil, false
	}
	result, err := exec.CommandContext(ctx, "ip", "neigh", "show", "dev", lan).Output()
	if err != nil {
		return nil, nil, false
	}
	ips := make(map[string]string)
	for _, line := range strings.Split(string(result), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || slices.Contains(fields, "FAILED") || slices.Contains(fields, "INCOMPLETE") {
			continue
		}
		index := slices.Index(fields, "lladdr")
		if index < 0 || index+1 >= len(fields) {
			continue
		}
		ip, ipErr := netip.ParseAddr(fields[0])
		mac, macErr := net.ParseMAC(fields[index+1])
		if ipErr == nil && !ip.IsMulticast() && macErr == nil && len(mac) == 6 {
			ips[ip.String()] = mac.String()
		}
	}
	var names map[string]string
	if iface, err := net.InterfaceByName(lan); err == nil {
		if addresses, err := iface.Addrs(); err == nil {
			names = addDHCPNeighbors(ips, addresses)
		}
	}
	return ips, names, true
}

func addDHCPNeighbors(ips map[string]string, addresses []net.Addr) map[string]string {
	leases, err := os.ReadFile("/tmp/dhcp.leases")
	if err != nil {
		return nil
	}
	names := make(map[string]string)
	now := time.Now().Unix()
	for _, line := range strings.Split(string(leases), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		expires, err := strconv.ParseInt(fields[0], 10, 64)
		ip, ipErr := netip.ParseAddr(fields[2])
		mac, macErr := net.ParseMAC(fields[1])
		if err != nil || (expires != 0 && expires <= now) || ipErr != nil || macErr != nil || len(mac) != 6 {
			continue
		}
		ipString, macString := ip.String(), mac.String()
		if current := ips[ipString]; current != "" && current != macString {
			continue
		}
		for _, address := range addresses {
			if prefix, err := netip.ParsePrefix(address.String()); err == nil && prefix.Contains(ip) {
				ips[ipString] = macString
				if len(fields) >= 4 && fields[3] != "*" && validName.MatchString(fields[3]) {
					names[macString] = fields[3]
				}
				break
			}
		}
	}
	return names
}

func resolverAddresses() []string {
	result := []string{}
	for _, path := range []string{"/etc/resolv.conf", "/tmp/resolv.conf.d/resolv.conf.auto"} {
		data, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "nameserver" {
				continue
			}
			addr, err := netip.ParseAddr(fields[1])
			if err == nil && !slices.Contains(result, addr.String()) {
				result = append(result, addr.String())
			}
		}
	}
	return result
}

func forwardingCounters(ctx context.Context) (result api.RouterForwarded) {
	if runtime.GOOS != "linux" {
		return
	}
	data, err := exec.CommandContext(ctx, "nft", "-j", "list", "counters", "table", "inet", "vpn_manager").Output()
	if err != nil {
		return
	}
	var output struct {
		Nftables []struct {
			Counter *struct {
				Name  string
				Bytes int
			}
		}
	}
	if json.Unmarshal(data, &output) != nil {
		return
	}
	count := 0
	for _, entry := range output.Nftables {
		if entry.Counter == nil {
			continue
		}
		switch entry.Counter.Name {
		case "forwarded_rx":
			result.Rx = entry.Counter.Bytes
			count++
		case "forwarded_tx":
			result.Tx = entry.Counter.Bytes
			count++
		}
	}
	result.Available = count == 2
	return
}

func (m *localSample) collect(ctx context.Context, rt *engine.Runtime, dataDir string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		read, cancel := context.WithTimeout(ctx, 2*time.Second)
		neighbors, names, neighborsOK := readNeighbors(read, rt.LAN)
		var forwarded api.RouterForwarded
		if state, _, _ := rt.Snapshot(); rt.TProxy && state == "running" {
			forwarded = forwardingCounters(read)
		}
		flows, truncated, err := readFlows(read)
		cancel()
		previous := m.at
		now := time.Now()
		seconds := now.Sub(previous).Seconds()
		dashboard := api.RouterDashboard{
			Forwarded: forwarded,
			SampledAt: now.UTC().Format(time.RFC3339Nano), Storage: storageVolumes(dataDir),
			DnsServers: resolverAddresses(), ClientsAvailable: neighborsOK, Clients: []api.RouterClient{},
			Connections: api.RouterConnections{Available: err == nil && neighborsOK, Truncated: truncated, Destinations: []api.RouterDestination{}},
		}
		clients := make(map[string]*api.RouterClient)
		for ip, mac := range neighbors {
			if clients[mac] == nil {
				clients[mac] = &api.RouterClient{Mac: mac}
				if hostname := names[mac]; hostname != "" {
					clients[mac].Hostname = &hostname
				}
			}
			clients[mac].Addresses = append(clients[mac].Addresses, ip)
		}
		if m.totals == nil {
			m.totals = make(map[string]engine.Traffic)
		}
		for mac := range m.totals {
			if clients[mac] == nil {
				delete(m.totals, mac)
			}
		}
		targets := make(map[string]*api.RouterDestination)
		added := 0
		tracked := make(map[string]flow)
		if dashboard.Connections.Available {
			for key, f := range flows {
				mac := neighbors[f.source]
				if mac == "" {
					continue
				}
				tracked[key] = f
				client := clients[mac]
				client.Sessions++
				if f.protocol == "tcp" {
					dashboard.Connections.Tcp++
				} else {
					dashboard.Connections.Udp++
				}
				if targets[f.destination] == nil {
					targets[f.destination] = &api.RouterDestination{Address: f.destination}
				}
				target := targets[f.destination]
				target.Sessions++
				target.Bytes += f.rx + f.tx
				prior, exists := m.flows[key]
				if !exists {
					added++
				}
				if !f.accounted {
					continue
				}
				dashboard.Connections.Accounting = true
				client.Accounted = true
				deltaRx, deltaTx := f.rx, f.tx
				if exists && prior.accounted {
					deltaRx = max(0, f.rx-prior.rx)
					deltaTx = max(0, f.tx-prior.tx)
				}
				total := m.totals[mac]
				total.Rx += deltaRx
				total.Tx += deltaTx
				m.totals[mac] = total
				if !previous.IsZero() {
					client.BytesPerSecond += float32(float64(deltaRx+deltaTx) / seconds)
				}
			}
			if !previous.IsZero() {
				dashboard.Connections.NewPerSecond = float32(float64(added) / seconds)
			}
			m.flows, m.at = tracked, now
		} else {
			m.at = time.Time{}
		}
		for _, dest := range targets {
			dashboard.Connections.Destinations = append(dashboard.Connections.Destinations, *dest)
		}
		sort.Slice(dashboard.Connections.Destinations, func(i, j int) bool {
			a, b := dashboard.Connections.Destinations[i], dashboard.Connections.Destinations[j]
			if a.Sessions != b.Sessions {
				return a.Sessions > b.Sessions
			}
			if a.Bytes != b.Bytes {
				return a.Bytes > b.Bytes
			}
			return a.Address < b.Address
		})
		dashboard.Connections.Destinations = dashboard.Connections.Destinations[:min(12, len(dashboard.Connections.Destinations))]
		for _, client := range clients {
			sort.Strings(client.Addresses)
			client.Rx = m.totals[client.Mac].Rx
			client.Tx = m.totals[client.Mac].Tx
			dashboard.Clients = append(dashboard.Clients, *client)
		}
		sort.Slice(dashboard.Clients, func(i, j int) bool { return dashboard.Clients[i].Mac < dashboard.Clients[j].Mac })
		m.Lock()
		m.dashboard = dashboard
		m.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *localSample) snapshot() api.RouterDashboard {
	m.RLock()
	defer m.RUnlock()
	value := m.dashboard
	value.Clients = append([]api.RouterClient{}, value.Clients...)
	return value
}
