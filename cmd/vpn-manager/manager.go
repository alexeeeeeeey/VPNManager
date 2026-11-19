package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/store"
)

const maxInput = 4 << 20
const maxProfiles = 512

var (
	errMissing = errors.New("запись не найдена")
	errActive  = errors.New("сначала остановите Xray или выберите другой профиль")
)

type manager struct {
	panelConfigPath string
	panelRestart    context.CancelFunc
	zapretLuaDir    string
	dataDir         string
	updater         panelUpdater
	dnsGateway      dnsGateway
	db              *sql.DB
	queries         *store.Queries
	runtime         *engine.Runtime
	writes          sync.Mutex
	metrics         *metrics
	routerMetrics   localSample
	externalMu      sync.Mutex
	externalCached  externalCache
}

func idFor(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		fmt.Fprintf(h, "%d:%s", len(value), value)
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:16])
}

func (m *manager) inventory(ctx context.Context) (api.Inventory, error) {
	result := api.Inventory{Profiles: []api.Profile{}, Subscriptions: []api.Subscription{}}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	q := m.queries.WithTx(tx)
	profiles, err := q.Profiles(ctx)
	if err != nil {
		return result, err
	}
	for _, p := range profiles {
		result.Profiles = append(result.Profiles, api.Profile{Id: p.ID, Name: p.Name, Kind: p.Kind, Host: p.Host, SourceId: p.SourceID, Available: p.Available != 0})
	}
	subscriptions, err := q.Subscriptions(ctx)
	if err != nil {
		return result, err
	}
	for _, s := range subscriptions {
		result.Subscriptions = append(result.Subscriptions, api.Subscription{Id: s.ID, Name: s.Name, RefreshHours: int(s.RefreshHours), UpdatedAt: s.UpdatedAt, Error: s.Error})
	}
	return result, tx.Commit()
}

func (m *manager) active(ctx context.Context) string {
	id, _ := m.queries.Setting(ctx, "active")
	return id
}

const directXrayConfig = `{"outbounds":[{"tag":"direct","protocol":"freedom"}]}`

func (m *manager) xrayWanted(ctx context.Context) bool {
	value, err := m.queries.Setting(ctx, "xray_enabled")
	if errors.Is(err, sql.ErrNoRows) {
		return m.active(ctx) != ""
	}
	return err == nil && value == "1"
}

func (m *manager) xrayConfig(ctx context.Context, id string) (string, error) {
	if id == "" {
		return directXrayConfig, nil
	}
	profile, err := m.queries.ProfileByID(ctx, id)
	return profile.Config, err
}

func (m *manager) selectXrayProfile(ctx context.Context, id string, enabled bool) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := m.queries.WithTx(tx)
	if err := q.SetSetting(ctx, store.SetSettingParams{Key: "active", Value: id}); err != nil {
		return err
	}
	value := "0"
	if enabled {
		value = "1"
	}
	if err := q.SetSetting(ctx, store.SetSettingParams{Key: "xray_enabled", Value: value}); err != nil {
		return err
	}
	return tx.Commit()
}

func fetch(ctx context.Context, source string) ([]engine.Profile, error) {
	client := http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(req *http.Request, prev []*http.Request) error {
			if len(prev) > 5 || (prev[0].URL.Scheme == "https" && req.URL.Scheme != "https") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, errors.New("некорректный URL подписки")
	}
	req.Header.Set("User-Agent", "Happ/3.0 VPN-Manager/1.0")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("не удалось загрузить подписку")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервер подписки вернул HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxInput+1))
	if err != nil || len(data) > maxInput {
		return nil, errors.New("не удалось прочитать подписку или размер больше 4 МБ")
	}
	return engine.ParseSubscription(string(data))
}

func (m *manager) save(ctx context.Context, profiles []engine.Profile, sub *store.Subscription, customName string) error {
	if len(profiles) > maxProfiles {
		return fmt.Errorf("максимум %d профилей за импорт", maxProfiles)
	}
	for _, p := range profiles {
		if _, err := engine.Build(p.Config, 1080, 0, 0, nil); err != nil {
			return err
		}
	}
	m.writes.Lock()
	defer m.writes.Unlock()
	return m.saveLocked(ctx, profiles, sub, customName)
}

func (m *manager) saveLocked(ctx context.Context, profiles []engine.Profile, sub *store.Subscription, customName string) (result error) {
	active := m.active(ctx)
	nextActive := active
	if sub != nil && active != "" && len(profiles) > 0 {
		current, err := m.queries.ProfileByID(ctx, active)
		if err != nil {
			return err
		}
		if current.SourceID == sub.ID {
			next := profiles[0]
			for _, candidate := range profiles {
				if candidate.Name == current.Name {
					next = candidate
					break
				}
			}
			nextActive = idFor(sub.ID, next.Identity)
			if next.Config != current.Config && m.xrayWanted(ctx) {
				if err := m.applyProfile(ctx, next.Config); err != nil {
					return err
				}
				defer func() {
					if result != nil {
						restore, cancel := context.WithTimeout(context.Background(), 12*time.Second)
						defer cancel()
						result = errors.Join(result, m.applyProfile(restore, current.Config))
					}
				}()
			}
		}
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := m.queries.WithTx(tx)
	source := ""
	if sub != nil {
		source = sub.ID
		if err = q.UpsertSubscription(ctx, store.UpsertSubscriptionParams{ID: source, Name: sub.Name, Url: sub.Url, RefreshHours: sub.RefreshHours}); err != nil {
			return err
		}
		if err = q.MarkMissing(ctx, source); err != nil {
			return err
		}
	}
	for index, p := range profiles {
		if customName != "" && len(profiles) == 1 {
			p.Name = customName
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("Xray · %d", index+1)
		}
		var position int64
		if sub != nil {
			position = int64(index + 1)
		}
		if err = q.UpsertProfile(ctx, store.UpsertProfileParams{ID: idFor(source, p.Identity), Name: p.Name, Kind: p.Kind, Host: p.Host, SourceID: source, Config: p.Config, SourcePosition: position}); err != nil {
			return err
		}
	}
	if sub != nil {
		if nextActive != active {
			if err = q.SetSetting(ctx, store.SetSettingParams{Key: "active", Value: nextActive}); err != nil {
				return err
			}
		}
		if err = q.DeleteMissing(ctx, store.DeleteMissingParams{SourceID: source, ID: nextActive}); err != nil {
			return err
		}
		now := time.Now()
		if err = q.SubscriptionResult(ctx, store.SubscriptionResultParams{ID: source, UpdatedAt: now.UTC().Format(time.RFC3339Nano), AttemptedAt: now.Unix()}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *manager) refresh(ctx context.Context, id string) error {
	sub, err := m.queries.SubscriptionByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errMissing
	}
	if err != nil {
		return err
	}
	profiles, result := fetch(ctx, sub.Url)
	if result == nil {
		if len(profiles) > maxProfiles {
			result = fmt.Errorf("максимум %d профилей", maxProfiles)
		}
		for _, p := range profiles {
			if _, err := engine.Build(p.Config, 1080, 0, 0, nil); err != nil {
				result = err
				break
			}
		}
	}
	m.writes.Lock()
	defer m.writes.Unlock()
	latest, err := m.queries.SubscriptionByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errMissing
	}
	if err != nil {
		return err
	}
	if latest.AttemptedAt != sub.AttemptedAt || latest.UpdatedAt != sub.UpdatedAt {
		return nil
	}
	if result != nil {
		return errors.Join(result, m.queries.SubscriptionResult(ctx, store.SubscriptionResultParams{ID: id, UpdatedAt: latest.UpdatedAt, AttemptedAt: time.Now().Unix(), Error: result.Error()}))
	}
	return m.saveLocked(ctx, profiles, &latest, "")
}

func (m *manager) importInput(ctx context.Context, input api.Input) error {
	value := strings.TrimSpace(input.Input)
	if !strings.ContainsAny(value, "\n\r") && (strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://")) {
		source, err := url.Parse(value)
		if err != nil || source.Hostname() == "" || source.User != nil {
			return errors.New("некорректный URL подписки")
		}
		name := strings.TrimSpace(input.Name)
		if name == "" {
			name = source.Hostname()
		}
		sub := store.Subscription{ID: idFor(source.String()), Name: name, Url: source.String(), RefreshHours: int64(input.RefreshHours)}
		profiles, err := fetch(ctx, sub.Url)
		if err != nil {
			return err
		}
		return m.save(ctx, profiles, &sub, "")
	}
	profiles, err := engine.Parse(value)
	if err != nil {
		return err
	}
	return m.save(ctx, profiles, nil, strings.TrimSpace(input.Name))
}

func (m *manager) activate(ctx context.Context, id string) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	p, err := m.queries.ProfileByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errMissing
	}
	if err != nil {
		return err
	}
	old := m.active(ctx)
	wasEnabled := m.xrayWanted(ctx)
	if p.Available == 0 && old != id {
		return errMissing
	}
	if wasEnabled {
		if err = m.applyProfile(ctx, p.Config); err != nil {
			return err
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = m.selectXrayProfile(persist, id, wasEnabled); err != nil {
		return errors.Join(err, m.restoreSelection(persist, old, wasEnabled))
	}
	return err
}

func (m *manager) stop(ctx context.Context) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	if err := m.queries.SetSetting(ctx, store.SetSettingParams{Key: "xray_enabled", Value: "0"}); err != nil {
		return err
	}
	m.runtime.Stop()
	return nil
}

func (m *manager) restart(ctx context.Context) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	id := m.active(ctx)
	wasEnabled := m.xrayWanted(ctx)
	raw, err := m.xrayConfig(ctx, id)
	if err != nil {
		return err
	}
	if err = m.applyProfile(ctx, raw); err != nil {
		return err
	}
	persist, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err = m.selectXrayProfile(persist, id, true); err != nil {
		return errors.Join(err, m.restoreSelection(persist, id, wasEnabled))
	}
	return nil
}

func (m *manager) clearXrayProfile(ctx context.Context) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	id, enabled := m.active(ctx), m.xrayWanted(ctx)
	if id == "" {
		return nil
	}
	if enabled {
		if err := m.applyProfile(ctx, directXrayConfig); err != nil {
			return err
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := m.selectXrayProfile(persist, "", enabled); err != nil {
		return errors.Join(err, m.restoreSelection(persist, id, enabled))
	}
	return nil
}

func (m *manager) restoreSelection(ctx context.Context, id string, enabled bool) error {
	if !enabled {
		m.runtime.Stop()
		return nil
	}
	raw, err := m.xrayConfig(ctx, id)
	if err != nil {
		return err
	}
	return m.applyProfile(ctx, raw)
}

func (m *manager) remove(ctx context.Context, id string, source bool) error {
	m.writes.Lock()
	defer m.writes.Unlock()
	active := m.active(ctx)
	routing, err := m.routingSettings(ctx)
	if err != nil {
		return err
	}
	for _, preset := range routing.Presets {
		if preset.ProfileId == "" {
			continue
		}
		if !source && preset.ProfileId == id {
			return fmt.Errorf("VPN-профиль используется пресетом %q", preset.Name)
		}
		if source {
			profile, err := m.queries.ProfileByID(ctx, preset.ProfileId)
			if err == nil && profile.SourceID == id {
				return fmt.Errorf("подписка используется пресетом %q", preset.Name)
			}
		}
	}
	if !source {
		if id == active {
			return errActive
		}
		return m.queries.DeleteProfile(ctx, id)
	}
	if p, err := m.queries.ProfileByID(ctx, active); err == nil && p.SourceID == id {
		return errActive
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := m.queries.WithTx(tx)
	if err = q.DeleteSourceProfiles(ctx, id); err != nil {
		return err
	}
	if err = q.DeleteSubscription(ctx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *manager) edit(ctx context.Context, id, input string) error {
	profiles, err := engine.Parse(input)
	if err != nil {
		return err
	}
	if len(profiles) != 1 || profiles[0].Kind != "JSON" {
		return errors.New("ожидается один JSON-конфиг Xray")
	}
	raw := profiles[0].Config
	if _, err = engine.Build(raw, 1080, 0, 0, nil); err != nil {
		return err
	}
	m.writes.Lock()
	defer m.writes.Unlock()
	old, err := m.queries.ProfileByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errMissing
	}
	if err != nil {
		return err
	}
	active := id == m.active(ctx) && m.xrayWanted(ctx)
	if active {
		if err = m.applyProfile(ctx, raw); err != nil {
			return err
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err = m.queries.UpdateProfileConfig(persist, store.UpdateProfileConfigParams{Config: raw, ID: id}); err != nil && active {
		_ = m.runtime.Apply(persist, old.Config)
	}
	return err
}

func (m *manager) probes(ctx context.Context, ids []string) []api.Probe {
	result := make([]api.Probe, len(ids))
	tasks := make(chan int)
	var workers sync.WaitGroup
	for range min(len(ids), 4) {
		workers.Go(func() {
			for index := range tasks {
				id := ids[index]
				probe := api.Probe{Id: id}
				p, err := m.queries.ProfileByID(ctx, id)
				if err == nil {
					probe.Latency, err = m.runtime.Probe(ctx, p.Config)
					if ctx.Err() == nil {
						m.routerMetrics.recordProbe(id, p.Name, err)
					}
				}
				if err != nil {
					probe.Error = err.Error()
				}
				result[index] = probe
			}
		})
	}
	for i := range ids {
		tasks <- i
	}
	close(tasks)
	workers.Wait()
	return result
}
