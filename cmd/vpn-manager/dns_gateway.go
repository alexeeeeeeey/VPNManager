package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/miekg/dns"
)

type resolverChain struct {
	cfg  api.DNSSettings
	http *http.Client
}

type dnsGateway struct {
	hasVPNProfile func(context.Context) bool
	runtime       *engine.Runtime
	upstream      string
	chain         atomic.Pointer[resolverChain]
	listener      *dnsListener
}

type dnsListener struct {
	tcp, udp *dns.Server
	port     int
}

func loopbackPort(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func bindDNS(port int, handler dns.Handler) (*dnsListener, error) {
	tcp, err := net.Listen("tcp4", loopbackPort(port))
	if err != nil {
		return nil, err
	}
	number := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenPacket("udp4", loopbackPort(number))
	if err != nil {
		tcp.Close()
		return nil, err
	}
	return &dnsListener{
		tcp:  &dns.Server{Listener: tcp, Handler: handler, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second},
		udp:  &dns.Server{PacketConn: udp, Handler: handler, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second},
		port: number,
	}, nil
}

func (l *dnsListener) closeSockets() {
	l.tcp.Listener.Close()
	l.udp.PacketConn.Close()
}

func (l *dnsListener) start() {
	for _, server := range []*dns.Server{l.tcp, l.udp} {
		ready := make(chan struct{})
		server.NotifyStartedFunc = func() { close(ready) }
		done := make(chan struct{})
		go func() { defer close(done); _ = server.ActivateAndServe() }()
		select {
		case <-ready:
		case <-done:
		}
	}
}

func (l *dnsListener) stop() {
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = l.tcp.ShutdownContext(ctx)
	_ = l.udp.ShutdownContext(ctx)
	l.closeSockets()
}

func directAddress(value string) string {
	if addr, err := netip.ParseAddr(value); err == nil {
		return net.JoinHostPort(addr.String(), "53")
	}
	addr, _ := netip.ParseAddrPort(value)
	return addr.String()
}

func newChain(cfg api.DNSSettings) *resolverChain {
	transport := &http.Transport{IdleConnTimeout: 20 * time.Second, ForceAttemptHTTP2: true}
	if len(cfg.Direct) > 0 {
		// DNS for DoH endpoint names uses the configured bootstrap resolver, avoiding dnsmasq loops.
		bootstrap := directAddress(cfg.Direct[0])
		dial := &net.Dialer{Resolver: &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, bootstrap)
		}}}
		transport.DialContext = dial.DialContext
	}
	return &resolverChain{cfg: cfg, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func usableDNS(query, answer *dns.Msg) bool {
	if answer == nil || !answer.Response || answer.Truncated || answer.Id != query.Id || answer.Opcode != query.Opcode || len(answer.Question) != 1 {
		return false
	}
	a, b := answer.Question[0], query.Question[0]
	return a.Qclass == b.Qclass && a.Qtype == b.Qtype && dns.CanonicalName(a.Name) == dns.CanonicalName(b.Name) &&
		(answer.Rcode == dns.RcodeSuccess || answer.Rcode == dns.RcodeNameError)
}

func exchangeDNS(ctx context.Context, query *dns.Msg, address string) *dns.Msg {
	answer, _, err := (&dns.Client{Net: "udp", Timeout: 900 * time.Millisecond}).ExchangeContext(ctx, query, address)
	if err != nil {
		return nil
	}
	if answer.Truncated {
		answer, _, err = (&dns.Client{Net: "tcp", Timeout: 900 * time.Millisecond}).ExchangeContext(ctx, query, address)
		if err != nil {
			return nil
		}
	}
	return answer
}

func exchangeDoH(ctx context.Context, client *http.Client, query *dns.Msg, endpoint string) *dns.Msg {
	outgoing := query.Copy()
	outgoing.Id = 0
	payload, err := outgoing.Pack()
	if err != nil {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || contentType != "application/dns-message" {
		return nil
	}
	wire, err := io.ReadAll(io.LimitReader(response.Body, 65536))
	if err != nil || len(wire) > 65535 {
		return nil
	}
	answer := new(dns.Msg)
	if answer.Unpack(wire) != nil || !usableDNS(outgoing, answer) {
		return nil
	}
	answer.Id = query.Id
	return answer
}

func (g *dnsGateway) ServeDNS(writer dns.ResponseWriter, query *dns.Msg) {
	reply := new(dns.Msg)
	if query.Opcode != dns.OpcodeQuery || len(query.Question) != 1 {
		writer.WriteMsg(reply.SetRcode(query, dns.RcodeFormatError))
		return
	}
	chain := g.chain.Load()
	if chain != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if response := g.resolve(ctx, chain, query); response != nil {
			if _, udp := writer.RemoteAddr().(*net.UDPAddr); udp {
				size := 512
				if edns := query.IsEdns0(); edns != nil {
					size = max(512, min(4096, int(edns.UDPSize())))
				}
				response.Truncate(size)
			}
			writer.WriteMsg(response)
			return
		}
	}
	writer.WriteMsg(reply.SetRcode(query, dns.RcodeServerFailure))
}

func (g *dnsGateway) resolve(ctx context.Context, chain *resolverChain, query *dns.Msg) *dns.Msg {
	for _, stage := range chain.cfg.Stages {
		var endpoints []string
		switch stage {
		case api.Xray:
			if state, _, _ := g.runtime.Snapshot(); state == "running" && g.hasVPNProfile != nil && g.hasVPNProfile(ctx) {
				endpoints = []string{g.upstream}
			}
		case api.Doh:
			endpoints = chain.cfg.Doh
		case api.Direct:
			endpoints = chain.cfg.Direct
		}
		for _, endpoint := range endpoints {
			if ctx.Err() != nil {
				return nil
			}
			budget := min(1100*time.Millisecond, 1500*time.Millisecond/time.Duration(len(endpoints)))
			attempt, cancel := context.WithTimeout(ctx, budget)
			var reply *dns.Msg
			switch stage {
			case api.Doh:
				reply = exchangeDoH(attempt, chain.http, query, endpoint)
			case api.Direct:
				reply = exchangeDNS(attempt, query, directAddress(endpoint))
			default:
				reply = exchangeDNS(attempt, query, endpoint)
			}
			cancel()
			if usableDNS(query, reply) {
				return reply
			}
		}
	}
	return nil
}

func (m *manager) startDNS(cfg api.DNSSettings) error {
	g := &m.dnsGateway
	g.runtime = m.runtime
	g.hasVPNProfile = func(ctx context.Context) bool { return m.active(ctx) != "" }
	internal, err := bindDNS(0, nil)
	if err != nil {
		return fmt.Errorf("выбор внутреннего DNS-порта Xray: %w", err)
	}
	internal.closeSockets()
	g.upstream = loopbackPort(internal.port)
	m.runtime.SetResolverPort(internal.port)
	return g.configure(context.Background(), cfg)
}

func managedDNS(cfg api.DNSSettings) bool {
	return cfg.Enabled != nil && *cfg.Enabled
}

func (g *dnsGateway) configure(ctx context.Context, cfg api.DNSSettings) error {
	listener, err := g.prepare(cfg.ListenPort)
	if err != nil {
		return err
	}
	if !managedDNS(cfg) {
		if err := g.forwardDNS(ctx, 0); err != nil {
			if listener != nil {
				listener.closeSockets()
			}
			return err
		}
	}
	g.apply(cfg, listener)
	if managedDNS(cfg) {
		return g.forwardDNS(ctx, cfg.ListenPort)
	}
	return nil
}

func (g *dnsGateway) prepare(port int) (*dnsListener, error) {
	if port == 0 || (g.listener != nil && port == g.listener.port) {
		return nil, nil
	}
	if loopbackPort(port) == g.upstream {
		return nil, errors.New("порт занят внутренним DNS-входом Xray")
	}
	listener, err := bindDNS(port, g)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть DNS на %s: %w", loopbackPort(port), err)
	}
	return listener, nil
}

// apply is called with the manager's writes lock held, or at startup.
func (g *dnsGateway) apply(cfg api.DNSSettings, listener *dnsListener) {
	if old := g.chain.Swap(newChain(cfg)); old != nil {
		old.http.CloseIdleConnections()
	}
	if listener != nil {
		listener.start()
	}
	if listener != nil || cfg.ListenPort == 0 {
		old := g.listener
		g.listener = listener
		old.stop()
	}
}
