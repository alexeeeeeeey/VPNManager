package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func TestDNSGatewayFallsBackAndKeepsNXDOMAIN(t *testing.T) {
	var calls atomic.Int32
	direct, err := bindDNS(0, dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		calls.Add(1)
		_ = w.WriteMsg(new(dns.Msg).SetReply(q))
	}))
	require.NoError(t, err)
	direct.start()
	defer direct.stop()

	var rcode atomic.Int32
	rcode.Store(dns.RcodeServerFailure)
	doh := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if q.Unpack(raw) != nil || q.Id != 0 {
			http.Error(w, "invalid query", 400)
			return
		}
		answer, _ := new(dns.Msg).SetRcode(q, int(rcode.Load())).Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(answer)
	}))
	defer doh.Close()
	gateway := dnsGateway{runtime: engine.New(engine.Options{})}
	chain := newChain(api.DNSSettings{
		Stages: []api.DNSSettingsStages{api.Xray, api.Doh, api.Direct},
		Doh:    []string{doh.URL},
		Direct: []string{loopbackPort(direct.port)},
	})
	chain.http = doh.Client()
	gateway.chain.Store(chain)

	front, err := bindDNS(0, &gateway)
	require.NoError(t, err)
	front.start()
	defer front.stop()

	request := new(dns.Msg).SetQuestion("example.test.", dns.TypeA)
	for _, network := range []string{"udp", "tcp"} {
		answer, _, err := (&dns.Client{Net: network}).Exchange(request, loopbackPort(front.port))
		require.NoError(t, err)
		require.Equal(t, dns.RcodeSuccess, answer.Rcode)
	}
	require.EqualValues(t, 2, calls.Load())
	rcode.Store(dns.RcodeNameError)
	answer, _, err := (&dns.Client{}).Exchange(request, loopbackPort(front.port))
	require.NoError(t, err)
	require.Equal(t, request.Id, answer.Id)
	require.Equal(t, dns.RcodeNameError, answer.Rcode)
	require.EqualValues(t, 2, calls.Load())
}
