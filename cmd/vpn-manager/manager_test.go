package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/engine"
	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/store"
	"github.com/stretchr/testify/require"
)

func testManager(t *testing.T) *manager {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return &manager{db: db, queries: store.New(db), runtime: engine.New(defaults().Engine), metrics: &metrics{}}
}

func TestSubscriptionRefreshReselectsActiveProfile(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	var response atomic.Value
	response.Store("socks5://old.example:1080#Zulu\nsocks5://b.example:1080#Beta\nsocks5://a.example:1080#Alpha")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, response.Load().(string))
	}))
	defer provider.Close()
	require.NoError(t, m.importInput(ctx, api.Input{Input: provider.URL, RefreshHours: 2}))
	initial, err := m.inventory(ctx)
	require.NoError(t, err)
	require.Len(t, initial.Profiles, 3)
	require.Equal(t, "Zulu", initial.Profiles[0].Name)
	require.Equal(t, "Beta", initial.Profiles[1].Name)
	require.Equal(t, "Alpha", initial.Profiles[2].Name)
	require.Len(t, initial.Subscriptions, 1)
	old := initial.Profiles[0]
	id := initial.Subscriptions[0].Id
	require.NoError(t, m.selectXrayProfile(ctx, old.Id, false))

	response.Store("invalid content")
	require.Error(t, m.refresh(ctx, id))
	unchanged, err := m.inventory(ctx)
	require.NoError(t, err)
	require.Equal(t, initial.Profiles, unchanged.Profiles)
	require.NotEmpty(t, unchanged.Subscriptions[0].Error)

	response.Store("socks5://a.example:1080#Alpha\nsocks5://new.example:1080#Zulu")
	require.NoError(t, m.refresh(ctx, id))
	selected, err := m.queries.ProfileByID(ctx, m.active(ctx))
	require.NoError(t, err)
	require.Equal(t, "new.example:1080", selected.Host)

	response.Store("socks5://a.example:1080#Alpha\nsocks5://b.example:1080#Beta")
	require.NoError(t, m.refresh(ctx, id))
	updated, err := m.inventory(ctx)
	require.NoError(t, err)
	require.Len(t, updated.Profiles, 2)
	require.Equal(t, "Alpha", updated.Profiles[0].Name)
	require.Equal(t, "Beta", updated.Profiles[1].Name)
	require.Equal(t, updated.Profiles[0].Id, m.active(ctx))
	require.Empty(t, updated.Subscriptions[0].Error)
	require.ErrorIs(t, m.remove(ctx, id, true), errActive)
}

func TestRouterAuthenticationAndSPAFallback(t *testing.T) {
	m := testManager(t)
	var assets fs.FS = fstest.MapFS{"index.html": {Data: []byte("dashboard")}}
	cfg := defaults()
	cfg.Password = "local-test"
	r := server{m}.router(cfg, assets)
	form := url.Values{"username": {cfg.Username}, "password": {cfg.Password}}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	session := httptest.NewRecorder()
	r.ServeHTTP(session, login)
	require.Equal(t, http.StatusSeeOther, session.Code)
	cookies := session.Result().Cookies()
	require.Len(t, cookies, 1)
	require.True(t, cookies[0].HttpOnly)
	for _, test := range []struct {
		path   string
		auth   bool
		status int
	}{
		{"/api/status", false, 401}, {"/api/status", true, 200}, {"/connections", false, 303}, {"/connections", true, 200}, {"/api/not-found", true, 404},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		if test.auth {
			req.AddCookie(cookies[0])
		}
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, req)
		require.Equal(t, test.status, recorder.Code, test.path)
		require.Empty(t, recorder.Header().Get("WWW-Authenticate"))
	}
	logout := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logout.AddCookie(cookies[0])
	r.ServeHTTP(httptest.NewRecorder(), logout)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.AddCookie(cookies[0])
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestChooseAndClearProfileWithoutStartingXray(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	require.NoError(t, m.importInput(ctx, api.Input{Input: "socks5://vpn.example:1080#VPN"}))
	inventory, err := m.inventory(ctx)
	require.NoError(t, err)
	id := inventory.Profiles[0].Id
	require.NoError(t, m.activate(ctx, id))
	require.Equal(t, id, m.active(ctx))
	require.False(t, m.xrayWanted(ctx))
	require.NoError(t, m.stop(ctx))
	require.Equal(t, id, m.active(ctx))
	require.NoError(t, m.clearXrayProfile(ctx))
	require.Empty(t, m.active(ctx))
	require.False(t, m.xrayWanted(ctx))
	raw, err := m.xrayConfig(ctx, "")
	require.NoError(t, err)
	require.Equal(t, directXrayConfig, raw)
}

func TestLegacyImportPreservesSubscriptionAndConfig(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	jsonDir := filepath.Join(dir, "json-subscriptions", "deadbeef")
	require.NoError(t, os.MkdirAll(jsonDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(jsonDir, "outbounds-1"), []byte(`{"tag":"proxy","protocol":"freedom"}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config"), []byte("SUBSCRIPTION_REFRESH_HOURS=\"12\"\n"), 0600))
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	legacy := fmt.Sprintf("old-id|1|%s|%s|%s\n", encode("Old profile"), encode("jsoncfg://deadbeef/1"), encode("https://provider.example/feed"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "links"), []byte(legacy), 0600))
	count, err := m.importLegacy(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	inventory, err := m.inventory(ctx)
	require.NoError(t, err)
	require.Len(t, inventory.Profiles, 1)
	require.Len(t, inventory.Subscriptions, 1)
	require.Equal(t, "Old profile", inventory.Profiles[0].Name)
	require.Equal(t, inventory.Subscriptions[0].Id, inventory.Profiles[0].SourceId)
	require.Equal(t, 12, inventory.Subscriptions[0].RefreshHours)
	require.Empty(t, m.active(ctx))
}
