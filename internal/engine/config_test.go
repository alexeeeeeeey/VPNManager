package engine

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSubscriptions(t *testing.T) {
	raw := "vless://test@example.net:443?security=reality&pbk=abc&type=grpc&serviceName=svc#Test\nhy2://secret@example.net?obfs=salamander&obfs-password=pass#QUIC"
	profiles, err := Parse(base64.RawURLEncoding.EncodeToString([]byte(raw)))
	require.NoError(t, err)
	require.Len(t, profiles, 2)
	require.Equal(t, "VLESS", profiles[0].Kind)
	require.Equal(t, "example.net:443", profiles[0].Host)
	require.Contains(t, profiles[0].Config, `"serviceName":"svc"`)
	require.Equal(t, "Hysteria2", profiles[1].Kind)
	require.Contains(t, profiles[1].Config, `"type":"salamander"`)
	_, err = Parse("vless://test@example.net:70000")
	require.Error(t, err)
	available, err := ParseSubscription("unsupported://ignored.example\n" + raw)
	require.NoError(t, err)
	require.Equal(t, profiles, available)
}

func TestBuildRetainsProviderRouting(t *testing.T) {
	configs, err := Parse(`[{"remarks":"Provider","inbounds":[{"tag":"old","listen":"0.0.0.0","port":9999}],"outbounds":[{"tag":"a","protocol":"freedom"}],"routing":{"balancers":[{"tag":"b","selector":["a"]}],"rules":[{"type":"field","inboundTag":["old"],"balancerTag":"b"}]},"dns":{"servers":["1.1.1.1"]}}]`)
	require.NoError(t, err)
	data, err := Build(configs[0].Config, 10818, 10808, 10085, []string{"domain:example.org"})
	require.NoError(t, err)
	require.True(t, json.Valid(data))
	require.NotContains(t, string(data), "9999")
	require.Contains(t, string(data), "manager-tproxy")
	require.Contains(t, string(data), "manager-socks")
	require.Contains(t, string(data), `"balancerTag": "b"`)
	require.Contains(t, string(data), `"listen": "127.0.0.1:10085"`)
	require.Contains(t, string(data), `"1.1.1.1"`)
}
