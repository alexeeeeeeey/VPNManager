package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Object = map[string]any

type Profile struct {
	Name, Kind, Host, Identity, Config string
}

func Parse(input string) ([]Profile, error) {
	return parse(input, false)
}

func ParseSubscription(input string) ([]Profile, error) {
	return parse(input, true)
}

func parse(input string, subscription bool) ([]Profile, error) {
	input = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "\ufeff"))
	if input == "" {
		return nil, errors.New("пустой список профилей")
	}
	if input[0] == '{' || input[0] == '[' {
		configs := []json.RawMessage{json.RawMessage(input)}
		if input[0] == '[' {
			if err := json.Unmarshal([]byte(input), &configs); err != nil {
				return nil, err
			}
		}
		profiles := make([]Profile, 0, len(configs))
		for _, raw := range configs {
			var obj Object
			if err := json.Unmarshal(raw, &obj); err != nil {
				return nil, err
			}
			outbounds, ok := obj["outbounds"].([]any)
			if !ok || len(outbounds) == 0 {
				return nil, errors.New("в JSON отсутствуют outbounds")
			}
			name, _ := obj["remarks"].(string)
			if name == "" {
				name, _ = obj["name"].(string)
			}
			compact, _ := json.Marshal(obj)
			profiles = append(profiles, Profile{name, "JSON", "", string(compact), string(compact)})
		}
		if len(profiles) == 0 {
			return nil, errors.New("пустой список профилей")
		}
		return profiles, nil
	}
	if !strings.Contains(input, "://") {
		encoded := strings.Join(strings.Fields(input), "")
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			decoded, err := encoding.DecodeString(encoded)
			if err == nil && (bytes.Contains(decoded, []byte("://")) || bytes.HasPrefix(bytes.TrimSpace(decoded), []byte("{")) || bytes.HasPrefix(bytes.TrimSpace(decoded), []byte("["))) {
				return parse(string(decoded), subscription)
			}
		}
	}
	var profiles []Profile
	for index, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if subscription {
			if scheme, _, ok := strings.Cut(line, "://"); ok {
				switch strings.ToLower(scheme) {
				case "vless", "hysteria2", "hy2", "socks", "socks5":
				default:
					continue
				}
			}
		}
		profile, err := parseLink(line)
		if err != nil {
			return nil, fmt.Errorf("строка %d: %w", index+1, err)
		}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return nil, errors.New("пустой список профилей")
	}
	return profiles, nil
}

func parseLink(raw string) (Profile, error) {
	var p Profile
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return p, errors.New("некорректная ссылка подключения")
	}
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if u.Port() == "" {
		port = 443
		if u.Scheme == "socks" || u.Scheme == "socks5" {
			port = 1080
		}
	} else if err != nil || port < 1 || port > 65535 {
		return p, errors.New("некорректный порт")
	}
	auth := ""
	password := ""
	if u.User != nil {
		auth = u.User.Username()
		password, _ = u.User.Password()
	}
	q := u.Query()
	server := Object{"address": host, "port": port}
	out := Object{"tag": "proxy"}
	sni := q.Get("sni")
	if sni == "" {
		sni = host
	}
	tls := Object{"serverName": sni, "allowInsecure": q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"}
	if fp := q.Get("fp"); fp != "" {
		tls["fingerprint"] = fp
	}
	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = strings.Split(alpn, ",")
	}
	pin := q.Get("pcs")
	if h := q.Get("pinSHA256"); h != "" {
		pin = h
	}
	if pin != "" {
		tls["pinnedPeerCertSha256"] = strings.ReplaceAll(pin, ":", "")
		tls["allowInsecure"] = false
	}
	if name := q.Get("vcn"); name != "" {
		tls["verifyPeerCertByName"] = name
	}
	switch u.Scheme {
	case "vless":
		if auth == "" {
			return p, errors.New("в VLESS отсутствует ID")
		}
		encryption := q.Get("encryption")
		if encryption == "" {
			encryption = "none"
		}
		server["users"] = []Object{{"id": auth, "encryption": encryption, "flow": q.Get("flow")}}
		out["protocol"] = "vless"
		out["settings"] = Object{"vnext": []Object{server}}
		network := q.Get("type")
		if network == "" || network == "tcp" {
			network = "raw"
		}
		security := q.Get("security")
		if security == "" {
			security = "none"
		}
		stream := Object{"network": network, "security": security}
		switch security {
		case "tls":
			stream["tlsSettings"] = tls
		case "reality":
			fp := q.Get("fp")
			if fp == "" {
				fp = "chrome"
			}
			stream["realitySettings"] = Object{"serverName": sni, "fingerprint": fp, "password": q.Get("pbk"), "shortId": q.Get("sid"), "spiderX": q.Get("spx")}
		}
		path := q.Get("path")
		if path == "" {
			path = "/"
		}
		switch network {
		case "ws", "websocket":
			stream["wsSettings"] = Object{"path": path, "host": q.Get("host")}
		case "grpc":
			stream["grpcSettings"] = Object{"serviceName": q.Get("serviceName"), "authority": q.Get("authority"), "multiMode": q.Get("mode") == "multi"}
		case "xhttp", "splithttp":
			settings := Object{"host": q.Get("host"), "path": path}
			if mode := q.Get("mode"); mode != "" {
				settings["mode"] = mode
			}
			if extra := q.Get("extra"); extra != "" {
				var obj Object
				if err := json.Unmarshal([]byte(extra), &obj); err != nil {
					return p, errors.New("некорректный XHTTP extra")
				}
				settings["extra"] = obj
			}
			stream["xhttpSettings"] = settings
		case "httpupgrade":
			stream["httpupgradeSettings"] = Object{"path": path, "host": q.Get("host")}
		case "raw":
			if q.Get("headerType") == "http" {
				stream["rawSettings"] = Object{"header": Object{"type": "http", "request": Object{"path": []string{path}, "headers": Object{"Host": strings.Split(q.Get("host"), ",")}}}}
			}
		}
		out["streamSettings"] = stream
		p.Kind = "VLESS"
	case "hysteria2", "hy2":
		if auth == "" {
			return p, errors.New("в Hysteria2 отсутствует пароль")
		}
		if password != "" {
			auth += ":" + password
		}
		server["version"] = 2
		tls["alpn"] = []string{"h3"}
		stream := Object{"network": "hysteria", "security": "tls", "tlsSettings": tls, "hysteriaSettings": Object{"version": 2, "auth": auth}}
		if q.Get("obfs") == "salamander" {
			stream["finalmask"] = Object{"udp": []Object{{"type": "salamander", "settings": Object{"password": q.Get("obfs-password")}}}}
		}
		out["protocol"] = "hysteria"
		out["settings"] = server
		out["streamSettings"] = stream
		p.Kind = "Hysteria2"
	case "socks", "socks5":
		if auth != "" {
			server["users"] = []Object{{"user": auth, "pass": password}}
		}
		out["protocol"] = "socks"
		out["settings"] = Object{"servers": []Object{server}}
		p.Kind = "SOCKS5"
	default:
		return p, errors.New("поддерживаются VLESS, Hysteria2, SOCKS5 и JSON Xray")
	}
	p.Name = u.Fragment
	if p.Name == "" {
		p.Name = host
	}
	p.Host = net.JoinHostPort(host, strconv.Itoa(port))
	config := Object{"outbounds": []Object{out}}
	if p.Kind == "Hysteria2" {
		config["vpnManagerHysteria"] = true
	}
	encoded, _ := json.Marshal(config)
	p.Config = string(encoded)
	p.Identity = p.Config + "\n" + p.Name
	return p, nil
}

func Build(raw string, socks, transparent, stats int, bypass []string) ([]byte, error) {
	return build(raw, socks, transparent, stats, 0, bypass)
}

func build(raw string, socks, transparent, stats, dnsPort int, bypass []string) ([]byte, error) {
	var source map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("ожидается объект Xray")
	}
	var outbounds []Object
	if err := json.Unmarshal(source["outbounds"], &outbounds); err != nil || len(outbounds) == 0 {
		return nil, errors.New("в конфигурации отсутствуют outbounds")
	}
	for _, outbound := range outbounds {
		if _, ok := outbound["protocol"].(string); !ok {
			return nil, errors.New("в outbound отсутствует protocol")
		}
	}
	var previous []Object
	if value := source["inbounds"]; len(value) > 0 {
		if err := json.Unmarshal(value, &previous); err != nil {
			return nil, err
		}
	}
	var oldTags []string
	for _, inbound := range previous {
		if tag, ok := inbound["tag"].(string); ok {
			oldTags = append(oldTags, tag)
		}
	}
	sniff := Object{"enabled": true, "destOverride": []string{"http", "tls", "quic", "fakedns"}, "routeOnly": true}
	inbounds := []Object{{"tag": "manager-socks", "listen": "127.0.0.1", "port": socks, "protocol": "socks", "sniffing": sniff, "settings": Object{"udp": true}}}
	if transparent > 0 {
		inbounds = append(inbounds, Object{"tag": "manager-tproxy", "port": transparent, "protocol": "dokodemo-door", "settings": Object{"network": "tcp,udp", "followRedirect": true}, "sniffing": sniff, "streamSettings": Object{"sockopt": Object{"tproxy": "tproxy"}}})
	}
	// Imported configurations retain outbound routing and DNS, but never open inbound listeners or log files.
	result := Object{"inbounds": inbounds, "outbounds": outbounds, "log": Object{"loglevel": "warning"}}
	for _, key := range []string{"dns", "fakedns", "observatory", "burstObservatory"} {
		if value := source[key]; len(value) > 0 {
			result[key] = value
		}
	}
	routing := Object{}
	if value := source["routing"]; len(value) > 0 {
		if err := json.Unmarshal(value, &routing); err != nil {
			return nil, err
		}
		if routing == nil {
			routing = Object{}
		}
	}
	var rules []any
	if dnsPort > 0 {
		inbounds = append(inbounds, Object{"tag": "manager-dns", "listen": "127.0.0.1", "port": dnsPort, "protocol": "dokodemo-door", "settings": Object{"address": "1.1.1.1", "port": 53, "network": "tcp,udp"}})
		outbounds = append(outbounds, Object{"tag": "manager-dns-out", "protocol": "dns"})
		result["inbounds"] = inbounds
		result["outbounds"] = outbounds
		if result["dns"] == nil {
			result["dns"] = Object{"servers": []string{"https://1.1.1.1/dns-query"}}
		}
		rules = append(rules, Object{"type": "field", "inboundTag": []string{"manager-dns"}, "outboundTag": "manager-dns-out"})
	}
	if stats > 0 {
		result["api"] = Object{"tag": "manager-api", "listen": fmt.Sprintf("127.0.0.1:%d", stats), "services": []string{"StatsService"}}
		result["stats"] = Object{}
		result["policy"] = Object{"system": Object{"statsInboundUplink": true, "statsInboundDownlink": true, "statsOutboundUplink": true, "statsOutboundDownlink": true}}
		tags := make(map[string]bool)
		for _, outbound := range outbounds {
			if tag, ok := outbound["tag"].(string); ok && tag != "" {
				tags[tag] = true
			}
		}
		for i, outbound := range outbounds {
			if outbound["tag"] == nil || outbound["tag"] == "" {
				tag := fmt.Sprintf("manager-out-%d", i+1)
				for tags[tag] {
					tag += "-auto"
				}
				tags[tag] = true
				outbound["tag"] = tag
			}
		}
	}
	if len(bypass) > 0 {
		outbounds = append(outbounds, Object{"tag": "manager-direct", "protocol": "freedom"})
		result["outbounds"] = outbounds
		rules = append(rules, Object{"type": "field", "domain": bypass, "outboundTag": "manager-direct"})
	}
	if imported, ok := routing["rules"].([]any); ok {
		for _, item := range imported {
			rule, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("некорректное правило маршрутизации")
			}
			if tags, ok := rule["inboundTag"].([]any); ok {
				var translated []string
				for _, old := range oldTags {
					for _, tag := range tags {
						if old == tag {
							translated = append(translated, "manager-socks")
							if transparent > 0 {
								translated = append(translated, "manager-tproxy")
							}
							break
						}
					}
					if len(translated) > 0 {
						break
					}
				}
				if len(translated) == 0 {
					continue
				}
				rule["inboundTag"] = translated
			}
			rules = append(rules, rule)
		}
	}
	if len(rules) > 0 {
		routing["rules"] = rules
	} else {
		delete(routing, "rules")
	}
	if len(routing) > 0 {
		result["routing"] = routing
	}
	return json.MarshalIndent(result, "", "  ")
}
