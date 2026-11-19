package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFlowUsesOriginalTupleAndDirectionalCounters(t *testing.T) {
	line := "ipv6 10 tcp 6 111 ESTABLISHED src=2001:db8::2 dst=2001:db8::10 sport=61020 dport=443 packets=3 bytes=120 src=2001:db8::10 dst=2001:db8::2 sport=443 dport=61020 packets=2 bytes=448 [ASSURED] mark=0"
	f, ok := parseFlow(line)
	require.True(t, ok)
	require.Equal(t, "2001:db8::2", f.source)
	require.Equal(t, "[2001:db8::10]:443", f.destination)
	require.Equal(t, 120, f.tx)
	require.Equal(t, 448, f.rx)
	require.True(t, f.accounted)

	flows, truncated, err := scanFlows(strings.NewReader("udp 17 20 src=192.168.1.2 dst=9.9.9.9 sport=60000 dport=53 src=9.9.9.9 dst=192.168.1.2 sport=53 dport=60000 [UNREPLIED]\ninvalid\n"))
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, flows, 1)
	require.False(t, flows["udp 192.168.1.2:60000 9.9.9.9:53"].accounted)
}

type externalTransport func(*http.Request) (*http.Response, error)

func (f externalTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestExternalRouteUsesIPv6EndpointAndParsesGeo(t *testing.T) {
	var hosts []string
	client := &http.Client{Transport: externalTransport(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Hostname())
		body := "2001:db8::1"
		if req.URL.Hostname() == "ipwho.is" {
			body = `{"success":true,"country":"Test","connection":{"asn":64512}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	value := externalRoute(context.Background(), client, "vpn", 6)
	require.Equal(t, []string{"api6.ipify.org", "ipwho.is"}, hosts)
	require.Equal(t, "2001:db8::1", value.Ip)
	require.Equal(t, "AS64512", value.Asn)
	require.Equal(t, "Test", value.Country)
	require.Empty(t, value.Error)
	require.NotEmpty(t, externalRoute(context.Background(), nil, "vpn", 4).Error)
}
