package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type Health struct {
	CheckedAt                     time.Time
	Milliseconds, Samples, Failed int
	Error                         string
}
type Outbound struct{ Tag, Protocol, Address string }
type Diagnostic struct {
	At            time.Time
	Kind, Message string
}
type Telemetry struct {
	Applied             time.Time
	Reconnected         time.Time
	Reconnects, Crashes int
	Health              Health
	Outbounds           []Outbound
	Rules               []string
	Events              []Diagnostic
}

func (r *Runtime) Telemetry() Telemetry { return r.status.Load().telemetry }

func (r *Runtime) VPNClient() *http.Client { return socksClient(r.SocksPort, 5*time.Second) }

func configTelemetry(raw string) ([]Outbound, []string) {
	var root struct {
		Outbounds []struct {
			Tag, Protocol string
			Settings      json.RawMessage
		}
		Routing struct{ Rules []json.RawMessage }
	}
	if json.Unmarshal([]byte(raw), &root) != nil {
		return nil, nil
	}
	result := make([]Outbound, 0, len(root.Outbounds))
	for _, o := range root.Outbounds {
		type target struct {
			Address string
			Port    int
		}
		var settings struct {
			target
			Vnext, Servers []target
			Peers          []struct{ Endpoint string }
		}
		_ = json.Unmarshal(o.Settings, &settings)
		addr := settings.target
		switch {
		case len(settings.Vnext) > 0:
			addr = settings.Vnext[0]
		case len(settings.Servers) > 0:
			addr = settings.Servers[0]
		}
		address := addr.Address
		if address != "" && addr.Port > 0 {
			address = net.JoinHostPort(address, fmt.Sprint(addr.Port))
		}
		if address == "" && len(settings.Peers) > 0 {
			address = settings.Peers[0].Endpoint
		}
		result = append(result, Outbound{o.Tag, o.Protocol, address})
	}
	rules := make([]string, 0, min(256, len(root.Routing.Rules)))
	for _, raw := range root.Routing.Rules[:min(256, len(root.Routing.Rules))] {
		var rule map[string]json.RawMessage
		if json.Unmarshal(raw, &rule) != nil {
			continue
		}
		var targets []string
		for _, k := range []string{"inboundTag", "domain", "ip", "source", "network", "protocol", "port", "outboundTag", "balancerTag"} {
			if v := rule[k]; len(v) > 0 {
				targets = append(targets, k+" "+string(v))
			}
		}
		line := strings.Join(targets, " · ")
		if len(line) > 800 {
			line = line[:800] + "…"
		}
		rules = append(rules, line)
	}
	return result, rules
}

func (r *Runtime) event(kind, message string) {
	events := append([]Diagnostic(nil), r.telemetry.Events...)
	events = append(events, Diagnostic{time.Now(), kind, message})
	r.telemetry.Events = events[max(0, len(events)-20):]
}

func (r *Runtime) recordHealth(now time.Time, elapsed int, err error) {
	h := r.telemetry.Health
	h.CheckedAt = now
	if len(r.samples) == 20 {
		r.samples = r.samples[1:]
	}
	r.samples = append(r.samples, err != nil)
	h.Samples = len(r.samples)
	h.Failed = 0
	for _, failed := range r.samples {
		if failed {
			h.Failed++
		}
	}
	h.Error = ""
	if err != nil {
		h.Error = err.Error()
	} else {
		h.Milliseconds = elapsed
	}
	r.telemetry.Health = h
}
