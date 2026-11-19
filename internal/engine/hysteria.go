package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func hysteriaSidecar(raw string, port int) (string, []byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw, nil, err
	}
	if string(root["vpnManagerHysteria"]) != "true" {
		return raw, nil, nil
	}
	var outbounds []struct {
		Tag      string
		Protocol string
		Settings struct {
			Address string
			Port    int
		}
		StreamSettings struct {
			TLSSettings struct {
				ServerName           string
				AllowInsecure        bool
				PinnedPeerCertSha256 string
			}
			HysteriaSettings struct{ Auth string }
			FinalMask        struct {
				UDP []struct {
					Type     string
					Settings struct{ Password string }
				}
			}
		}
	}
	if err := json.Unmarshal(root["outbounds"], &outbounds); err != nil {
		return raw, nil, err
	}
	if len(outbounds) != 1 || outbounds[0].Protocol != "hysteria" {
		return raw, nil, errors.New("для отдельного Hysteria требуется один hysteria outbound")
	}
	outbound := outbounds[0]
	transport := outbound.StreamSettings
	if outbound.Settings.Address == "" || outbound.Settings.Port < 1 || outbound.Settings.Port > 65535 || transport.HysteriaSettings.Auth == "" {
		return raw, nil, errors.New("в профиле Hysteria отсутствует адрес или пароль")
	}
	pin := transport.TLSSettings.PinnedPeerCertSha256
	if pin != "" {
		digest, err := hex.DecodeString(pin)
		if err != nil || len(digest) != 32 {
			return raw, nil, errors.New("Hysteria2 ожидает один SHA-256 отпечаток сертификата")
		}
	}
	settings := Object{
		"server": net.JoinHostPort(outbound.Settings.Address, strconv.Itoa(outbound.Settings.Port)),
		"auth":   transport.HysteriaSettings.Auth, "lazy": true,
		// Hysteria checks the pin separately from the system trust store.
		"tls":    Object{"sni": transport.TLSSettings.ServerName, "insecure": transport.TLSSettings.AllowInsecure || pin != "", "pinSHA256": pin},
		"socks5": Object{"listen": fmt.Sprintf("127.0.0.1:%d", port)},
	}
	masks := transport.FinalMask.UDP
	if len(masks) > 0 {
		if len(masks) != 1 || masks[0].Type != "salamander" {
			return raw, nil, errors.New("отдельный Hysteria поддерживает Salamander")
		}
		settings["obfs"] = Object{"type": "salamander", "salamander": Object{"password": masks[0].Settings.Password}}
	}
	root["outbounds"], _ = json.Marshal([]Object{{"tag": outbound.Tag, "protocol": "socks", "settings": Object{"servers": []Object{{"address": "127.0.0.1", "port": port}}}}})
	config, _ := json.Marshal(root)
	sidecar, _ := json.Marshal(settings)
	return string(config), sidecar, nil
}

func (r *Runtime) prepareSidecar(ctx context.Context, raw string, writer io.Writer) (string, *process, error) {
	if r.HysteriaBinary == "" {
		return raw, nil, nil
	}
	var marker struct{ VPNManagerHysteria bool }
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return raw, nil, err
	}
	if !marker.VPNManagerHysteria {
		return raw, nil, nil
	}
	port, err := freeTCPPort()
	if err != nil {
		return raw, nil, err
	}
	config, sidecar, err := hysteriaSidecar(raw, port)
	if err != nil {
		return raw, nil, err
	}
	path, err := writeTemp(r.TempDir, "hysteria-*.json", sidecar)
	if err != nil {
		return raw, nil, err
	}
	defer os.Remove(path)
	p, err := startProcess(writer, r.HysteriaBinary, "client", "-c", path)
	if err != nil {
		return raw, nil, fmt.Errorf("запуск Hysteria2: %w", err)
	}
	ready, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err = waitReady(ready, p, port); err != nil {
		p.stop()
		return raw, nil, fmt.Errorf("Hysteria2 не открыл SOCKS-порт: %w", err)
	}
	return config, p, nil
}
