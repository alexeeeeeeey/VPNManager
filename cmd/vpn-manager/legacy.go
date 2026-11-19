package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/store"
)

var oldJSONRef = regexp.MustCompile(`^jsoncfg://([0-9a-fA-F]+)/([1-9][0-9]*)$`)

func legacyJSON(dir, ref string) (string, error) {
	parts := oldJSONRef.FindStringSubmatch(ref)
	if parts == nil {
		return "", errors.New("некорректная ссылка на старый JSON")
	}
	root := filepath.Join(dir, "json-subscriptions", parts[1])
	result := engine.Object{}
	routing := engine.Object{}
	for _, name := range []string{"outbounds", "rules", "balancers", "observatory", "burst-observatory"} {
		raw, err := os.ReadFile(filepath.Join(root, name+"-"+parts[2]))
		if errors.Is(err, os.ErrNotExist) && name != "outbounds" {
			continue
		}
		if err != nil {
			return "", err
		}
		var values []json.RawMessage
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !json.Valid([]byte(line)) {
				return "", errors.New("повреждён JSON старого профиля")
			}
			values = append(values, json.RawMessage(line))
		}
		switch name {
		case "outbounds":
			for _, outbound := range []string{`{"tag":"direct","protocol":"freedom"}`, `{"tag":"block","protocol":"blackhole"}`} {
				values = append(values, json.RawMessage(outbound))
			}
			result[name] = values
		case "rules", "balancers":
			routing[name] = values
		case "observatory", "burst-observatory":
			if len(values) > 0 {
				if name == "burst-observatory" {
					name = "burstObservatory"
				}
				result[name] = values[0]
			}
		}
	}
	if len(routing) > 0 {
		result["routing"] = routing
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func (m *manager) importLegacy(ctx context.Context, dir string) (int, error) {
	f, err := os.Open(filepath.Join(dir, "links"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 4096), maxInput)
	hours := int64(0)
	if config, err := os.ReadFile(filepath.Join(dir, "config")); err == nil {
		for _, line := range strings.Split(string(config), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "SUBSCRIPTION_REFRESH_HOURS="); ok {
				hours, _ = strconv.ParseInt(strings.Trim(value, "\"'"), 10, 64)
				hours = min(max(0, hours), 720)
			}
		}
	}
	type entry struct {
		p      engine.Profile
		source *store.Subscription
	}
	var items []entry
	for index := 1; lines.Scan(); index++ {
		line := strings.TrimSpace(lines.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 5 {
			return 0, fmt.Errorf("некорректная запись links, строка %d", index)
		}
		if fields[1] != "1" {
			continue
		}
		decoded := make([]string, 3)
		for pos, raw := range fields[2:] {
			data, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return 0, fmt.Errorf("некорректный base64, строка %d", index)
			}
			decoded[pos] = string(data)
		}
		raw := decoded[1]
		if strings.HasPrefix(raw, "jsoncfg://") {
			raw, err = legacyJSON(dir, raw)
			if err != nil {
				return 0, fmt.Errorf("JSON профиля в строке %d: %w", index, err)
			}
		}
		profiles, err := engine.Parse(raw)
		if err != nil || len(profiles) != 1 {
			return 0, fmt.Errorf("некорректный профиль в строке %d", index)
		}
		profile := profiles[0]
		if decoded[0] != "" {
			profile.Name = decoded[0]
		}
		if profile.Name == "" {
			profile.Name = fmt.Sprintf("Xray · %d", index)
		}
		var sub *store.Subscription
		if u, err := url.Parse(decoded[2]); err == nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https") {
			sub = &store.Subscription{ID: idFor(u.String()), Name: u.Hostname(), Url: u.String(), RefreshHours: hours}
		}
		items = append(items, entry{profile, sub})
	}
	if err = lines.Err(); err != nil {
		return 0, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := m.queries.WithTx(tx)
	for _, item := range items {
		source := ""
		if item.source != nil {
			sub := item.source
			source = sub.ID
			if _, err := q.SubscriptionByID(ctx, source); err != nil {
				if err = q.UpsertSubscription(ctx, store.UpsertSubscriptionParams{ID: source, Name: sub.Name, Url: sub.Url, RefreshHours: sub.RefreshHours}); err != nil {
					return 0, err
				}
			}
		}
		p := item.p
		if err = q.UpsertProfile(ctx, store.UpsertProfileParams{ID: idFor(source, p.Identity), Name: p.Name, Kind: p.Kind, Host: p.Host, SourceID: source, Config: p.Config}); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit()
}
