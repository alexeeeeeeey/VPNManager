package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/store"
	"github.com/go-chi/chi/v5"
)

const autoUpdateKey = "auto_updates"

type autoUpdateSettings struct {
	Subscriptions int `json:"subscriptions"`
	Panel         int `json:"panel"`
	Zapret        int `json:"zapret"`
}

func (m *manager) autoUpdates(ctx context.Context) (autoUpdateSettings, error) {
	cfg := autoUpdateSettings{Subscriptions: 1, Panel: 24, Zapret: 24}
	err := m.readSetting(ctx, autoUpdateKey, &cfg)
	return cfg, err
}

func (s server) autoUpdateRoutes(r chi.Router) {
	r.Get("/api/panel/auto-updates", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.autoUpdates(r.Context())
		jsonResponse(w, cfg, err)
	})
	r.Put("/api/panel/auto-updates", func(w http.ResponseWriter, r *http.Request) {
		var cfg autoUpdateSettings
		if !decode(w, r, &cfg) {
			return
		}
		for _, hours := range []int{cfg.Subscriptions, cfg.Panel, cfg.Zapret} {
			if hours < 0 || hours > 720 {
				jsonResponse(w, nil, errors.New("интервал должен быть от 0 до 720 часов"))
				return
			}
		}
		jsonResponse(w, cfg, s.writeSetting(r.Context(), autoUpdateKey, cfg))
	})
}

func (m *manager) updateDue(ctx context.Context, key string, hours int) bool {
	value, err := m.queries.Setting(ctx, key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	last, _ := strconv.ParseInt(value, 10, 64)
	now := time.Now()
	if last != 0 && now.Unix()-last < int64(hours)*3600 {
		return false
	}
	if err := m.queries.SetSetting(ctx, store.SetSettingParams{Key: key, Value: strconv.FormatInt(now.Unix(), 10)}); err != nil {
		return false
	}
	return last != 0
}

func (m *manager) watchUpdates(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var nextInstall time.Time
	for {
		if zapretArchitecture() != "" && os.Geteuid() == 0 && time.Now().After(nextInstall) && m.writes.TryLock() {
			nextInstall = time.Now().Add(15 * time.Minute)
			if _, err := m.queries.Setting(ctx, zapretSettingsKey); errors.Is(err, sql.ErrNoRows) {
				install, cancel := context.WithTimeout(ctx, 2*time.Minute)
				_, err = m.updateZapret(install, zapretState{ZapretSettings: defaultZapretSettings()})
				cancel()
				if err != nil {
					slog.Warn("Установка Zapret2 по умолчанию", "error", err)
				}
			}
			m.writes.Unlock()
		}
		if cfg, err := m.autoUpdates(ctx); err == nil {
			m.runAutoUpdates(ctx, cfg)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *manager) runAutoUpdates(ctx context.Context, cfg autoUpdateSettings) {
	if cfg.Subscriptions > 0 {
		subs, _ := m.queries.Subscriptions(ctx)
		for _, sub := range subs {
			if ctx.Err() != nil {
				return
			}
			if time.Since(time.Unix(sub.AttemptedAt, 0)) >= time.Duration(cfg.Subscriptions)*time.Hour {
				_ = m.refresh(ctx, sub.ID)
			}
		}
	}
	if cfg.Zapret > 0 && zapretArchitecture() != "" && m.writes.TryLock() {
		state, err := m.loadZapret(ctx)
		if err == nil && state.Version != "" && m.updateDue(ctx, "auto_zapret_attempt", cfg.Zapret) {
			update, cancel := context.WithTimeout(ctx, 2*time.Minute)
			_, err = m.updateZapret(update, state)
			cancel()
			if err != nil {
				slog.Warn("Автообновление Zapret2", "error", err)
			}
		}
		m.writes.Unlock()
	}
	u := &m.updater
	if cfg.Panel <= 0 || u.executable == "" || releaseVersion(buildVersion) == "" || !u.installing.TryLock() {
		return
	}
	defer u.installing.Unlock()
	if !m.updateDue(ctx, "auto_panel_attempt", cfg.Panel) {
		return
	}
	update, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	status := u.status(update)
	if !status.Available {
		return
	}
	if err := u.install(update, status.Latest); err != nil {
		slog.Warn("Автообновление панели", "error", err)
		return
	}
	u.restart()
}
