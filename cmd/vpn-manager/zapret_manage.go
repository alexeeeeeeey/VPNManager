package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"golang.org/x/mod/semver"
)

const zapretRepo = "bol-van/zapret2"

type zapretState struct {
	api.ZapretSettings
	Version string `json:"installedVersion"`
}

type zapretRelease struct {
	version string
	digest  []byte
}

func defaultZapretSettings() api.ZapretSettings {
	return api.ZapretSettings{Enabled: true, Args: []string{
		"--lua-init=@{zapret}/lua/zapret-lib.lua", "--lua-init=@{zapret}/lua/zapret-antidpi.lua",
		"--filter-tcp=443", "--filter-l7=tls", "--payload=tls_client_hello",
		"--lua-desync=fake:blob=fake_default_tls:tcp_md5:tcp_seq=-10000",
		"--lua-desync=multidisorder:pos=1,midsld",
	}}
}

func (m *manager) loadZapret(ctx context.Context) (zapretState, error) {
	var saved struct {
		Binary  string   `json:"binary"`
		Args    []string `json:"args"`
		Enabled *bool    `json:"enabled"`
		Version string   `json:"installedVersion"`
	}
	err := m.readSetting(ctx, zapretSettingsKey, &saved)
	state := zapretState{ZapretSettings: api.ZapretSettings{Binary: saved.Binary, Args: saved.Args}, Version: saved.Version}
	if state.Args == nil {
		state.Args = []string{}
	}
	// Old settings were activated by referencing ZAPRET in a routing preset.
	state.Enabled = saved.Binary != "" && len(state.Args) > 0
	if saved.Enabled != nil {
		state.Enabled = *saved.Enabled
	}
	if state.Version != "" && releaseVersion(state.Version) != state.Version {
		return state, errors.New("некорректная сохранённая версия Zapret2")
	}
	return state, err
}

func (m *manager) zapretRoot() string { return filepath.Join(m.dataDir, "zapret2") }
func (m *manager) zapretDirectory(version string) string {
	return filepath.Join(m.zapretRoot(), version)
}
func (m *manager) zapretBinary(state zapretState) string {
	if state.Binary != "" {
		return state.Binary
	}
	if state.Version == "" {
		return ""
	}
	return filepath.Join(m.zapretDirectory(state.Version), "nfqws2")
}

func (m *manager) effectiveZapret(state zapretState) (api.ZapretSettings, error) {
	result := state.ZapretSettings
	result.Binary = m.zapretBinary(state)
	if result.Binary == "" {
		return result, errors.New("сначала установите Zapret2 или укажите путь к nfqws2")
	}
	if len(result.Args) == 0 {
		return result, errors.New("выберите или настройте стратегию Zapret2")
	}
	result.Args = append([]string(nil), result.Args...)
	var assets string
	for index, arg := range result.Args {
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--qnum", "--fwmark", "--daemon", "--dry-run", "--pidfile", "--help", "--version":
			return result, fmt.Errorf("опцию %s задаёт менеджер", flag)
		}
		if !strings.HasPrefix(arg, "--") {
			return result, errors.New("используйте формат --ключ или --ключ=значение")
		}
		if !strings.Contains(arg, "{zapret}") {
			continue
		}
		if state.Version == "" {
			return result, errors.New("для путей {zapret} нужна установка из панели")
		}
		if assets == "" {
			var err error
			assets, err = m.prepareZapretLua(state.Version)
			if err != nil {
				return result, fmt.Errorf("подготовка Lua-библиотек Zapret2: %w", err)
			}
		}
		result.Args[index] = strings.ReplaceAll(arg, "{zapret}", assets)
	}
	return result, nil
}

// nfqws2 opens Lua files after dropping privileges. Expose only the bundled scripts.
func (m *manager) prepareZapretLua(version string) (string, error) {
	if m.zapretLuaDir == "" {
		dir, err := os.MkdirTemp("", "vpn-manager-zapret-")
		if err != nil {
			return "", err
		}
		if err = os.Chmod(dir, 0711); err != nil {
			_ = os.Remove(dir)
			return "", err
		}
		m.zapretLuaDir = dir
	}
	target := filepath.Join(m.zapretLuaDir, version)
	if _, err := os.Stat(target); err == nil {
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	source := filepath.Join(m.zapretDirectory(version), "lua")
	files, err := os.ReadDir(source)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(m.zapretLuaDir, ".lua-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	lua := filepath.Join(stage, "lua")
	if err = os.Mkdir(lua, 0755); err != nil {
		return "", err
	}
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".lua") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, file.Name()))
		if err != nil {
			return "", err
		}
		dest := filepath.Join(lua, file.Name())
		if err = os.WriteFile(dest, data, 0644); err != nil {
			return "", err
		}
		if err = os.Chmod(dest, 0644); err != nil {
			return "", err
		}
	}
	if err = os.Chmod(lua, 0755); err != nil {
		return "", err
	}
	if err = os.Chmod(stage, 0711); err != nil {
		return "", err
	}
	return target, os.Rename(stage, target)
}

func (m *manager) removeZapretLua(version string) {
	if m.zapretLuaDir != "" {
		_ = os.RemoveAll(filepath.Join(m.zapretLuaDir, version))
	}
}

func zapretArchitecture() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	switch runtime.GOARCH {
	case "amd64":
		return "linux-x86_64"
	case "arm64":
		return "linux-arm64"
	}
	return ""
}

func latestZapret(ctx context.Context) (zapretRelease, error) {
	body, err := getRelease(ctx, "https://api.github.com/repos/"+zapretRepo+"/releases/latest")
	if err != nil {
		return zapretRelease{}, err
	}
	defer body.Close()
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct{ Name, Digest string }
	}
	if err = json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&release); err != nil {
		return zapretRelease{}, err
	}
	version := releaseVersion(release.Tag)
	if version == "" {
		return zapretRelease{}, errors.New("стабильный релиз Zapret2 недоступен")
	}
	for _, asset := range release.Assets {
		if asset.Name != "zapret2-v"+version+".tar.gz" || !strings.HasPrefix(asset.Digest, "sha256:") {
			continue
		}
		digest, err := hex.DecodeString(strings.TrimPrefix(asset.Digest, "sha256:"))
		if err == nil && len(digest) == sha256.Size {
			return zapretRelease{version, digest}, nil
		}
	}
	return zapretRelease{}, errors.New("релиз без архива с SHA-256")
}

func extractZapret(source io.Reader, target, prefix, architecture string) error {
	gz, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer gz.Close()
	archive := tar.NewReader(io.LimitReader(gz, 64<<20))
	binary := false
	files := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || !strings.HasPrefix(header.Name, prefix+"/") {
			continue
		}
		name := strings.TrimPrefix(header.Name, prefix+"/")
		mode := os.FileMode(0600)
		if name == "binaries/"+architecture+"/nfqws2" {
			name = "nfqws2"
			mode = 0700
		} else if path.Dir(name) == "lua" && strings.HasSuffix(name, ".lua") {
			// Only copy the Lua libraries alongside the binary.
		} else {
			continue
		}
		if name != path.Clean(name) || strings.Contains(name, "\\") || header.Size <= 0 || header.Size > 8<<20 || files >= 64 {
			return errors.New("некорректный файл в архиве Zapret2")
		}
		files++
		filePath := filepath.Join(target, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, err = io.CopyN(file, archive, header.Size)
		if err = errors.Join(err, file.Close()); err != nil {
			return err
		}
		binary = binary || name == "nfqws2"
	}
	if !binary {
		return errors.New("в архиве нет nfqws2 для этой архитектуры")
	}
	for _, library := range []string{"zapret-lib.lua", "zapret-antidpi.lua"} {
		if _, err := os.Stat(filepath.Join(target, "lua", library)); err != nil {
			return errors.New("в архиве нет библиотек Zapret2")
		}
	}
	return nil
}

func (m *manager) downloadZapret(ctx context.Context, release zapretRelease) (string, error) {
	arch := zapretArchitecture()
	if arch == "" {
		return "", errors.New("установка доступна на Linux amd64/arm64")
	}
	root := m.zapretRoot()
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	prefix := "zapret2-v" + release.version
	source, err := getRelease(ctx, "https://github.com/"+zapretRepo+"/releases/download/v"+release.version+"/"+prefix+".tar.gz")
	if err != nil {
		return "", err
	}
	defer source.Close()
	archive, err := os.CreateTemp("", "zapret2-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(archive, digest), io.LimitReader(source, maximumArchive+1))
	if err != nil {
		return "", err
	}
	if size > maximumArchive || !bytes.Equal(digest.Sum(nil), release.digest) {
		return "", errors.New("контрольная сумма архива Zapret2 не совпала")
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err = extractZapret(archive, stage, prefix, arch); err != nil {
		return "", err
	}
	dest := m.zapretDirectory(release.version)
	if err = ctx.Err(); err != nil {
		return "", err
	}
	// This version is not selected. Replace files left by an interrupted install.
	if err = os.RemoveAll(dest); err != nil {
		return "", err
	}
	if err = os.Rename(stage, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (m *manager) changeZapret(ctx context.Context, old, next zapretState) error {
	var settings api.ZapretSettings
	if next.Enabled {
		var err error
		settings, err = m.effectiveZapret(next)
		if err != nil {
			return err
		}
	}
	cfg, err := m.routingSettings(ctx)
	if err != nil {
		return err
	}
	plan, err := m.prepareRoutingWithZapret(ctx, &cfg, &next)
	if err != nil {
		return err
	}
	raw := ""
	live := m.xrayWanted(ctx) && m.runtime.TProxy
	if next.Enabled && (!live || !plan.UsesZapret) {
		if err = m.runtime.ValidateZapret(ctx, settings); err != nil {
			return err
		}
	}
	if live && (old.Enabled || next.Enabled) {
		raw, err = m.xrayConfig(ctx, m.active(ctx))
		if err != nil {
			return err
		}
		if err = m.applyRoutingConfig(ctx, raw, plan); err != nil {
			return err
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err = m.writeSetting(persist, zapretSettingsKey, next); err != nil && raw != "" {
		previous, restoreErr := m.prepareRoutingWithZapret(persist, &cfg, &old)
		if restoreErr == nil {
			restoreErr = m.applyRoutingConfig(persist, raw, previous)
		}
		return errors.Join(err, restoreErr)
	}
	return err
}

func (m *manager) zapretStatus(state zapretState) api.ZapretStatus {
	status := api.ZapretStatus{Supported: zapretArchitecture() != "", Installed: state.Version, Running: m.runtime.ZapretRunning()}
	if state.Version != "" {
		path := m.zapretBinary(zapretState{Version: state.Version})
		status.BinaryPath = &path
	}
	return status
}

func (s server) GetZapretStatus(w http.ResponseWriter, r *http.Request, params api.GetZapretStatusParams) {
	state, err := s.loadZapret(r.Context())
	result := s.zapretStatus(state)
	if err == nil && result.Supported && params.Check != nil && *params.Check {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		if release, fetchErr := latestZapret(ctx); fetchErr == nil {
			result.Latest = release.version
			result.UpdateAvailable = state.Version != "" && semver.Compare("v"+release.version, "v"+state.Version) > 0
		} else {
			err = fetchErr
		}
	}
	jsonResponse(w, result, err)
}

func (s server) ManageZapret(w http.ResponseWriter, r *http.Request) {
	var input api.ZapretAction
	if !decode(w, r, &input) {
		return
	}
	if zapretArchitecture() == "" {
		jsonResponse(w, nil, errors.New("управление nfqws2 доступно на Linux amd64/arm64"))
		return
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	ctx := r.Context()
	old, err := s.loadZapret(ctx)
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	next := old
	switch input.Action {
	case "install":
		next, err = s.updateZapret(ctx, old)
	case "remove":
		if old.Version != "" {
			next.Version = ""
			if next.Binary == "" {
				next.Enabled = false
			}
			for _, arg := range next.Args {
				if strings.Contains(arg, "{zapret}") {
					next.Enabled = false
					break
				}
			}
			err = s.changeZapret(ctx, old, next)
			if err == nil {
				err = os.RemoveAll(s.zapretDirectory(old.Version))
				s.removeZapretLua(old.Version)
			}
		}
	case "check":
		var cfg api.ZapretSettings
		cfg, err = s.effectiveZapret(old)
		if err == nil {
			err = s.runtime.ValidateZapret(ctx, cfg)
		}
	default:
		err = errors.New("неизвестное действие Zapret2")
	}
	jsonResponse(w, s.zapretStatus(next), err)
}

// updateZapret runs with the manager's writes lock held.
func (m *manager) updateZapret(ctx context.Context, old zapretState) (zapretState, error) {
	release, err := latestZapret(ctx)
	if err != nil || (old.Version != "" && semver.Compare("v"+release.version, "v"+old.Version) <= 0) {
		return old, err
	}
	installed, err := m.downloadZapret(ctx, release)
	if err != nil {
		return old, err
	}
	next := old
	next.Version = release.version
	if err := m.changeZapret(ctx, old, next); err != nil {
		_ = os.RemoveAll(installed)
		m.removeZapretLua(release.version)
		return old, err
	}
	if old.Version != "" {
		_ = os.RemoveAll(m.zapretDirectory(old.Version))
		m.removeZapretLua(old.Version)
	}
	return next, nil
}

func (s server) ProbeZapret(w http.ResponseWriter, r *http.Request) {
	var input api.ZapretProbeInput
	if !decode(w, r, &input) {
		return
	}
	address, err := zapretTarget(input.Url)
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	status, ms, err := s.runtime.ProbeZapret(r.Context(), address)
	jsonResponse(w, api.ZapretProbe{Url: address, Status: status, Latency: ms}, err)
}

func zapretTarget(address string) (string, error) {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) != nil || !strings.Contains(parsed.Hostname(), ".") {
		return "", errors.New("укажите домен или HTTPS-адрес домена")
	}
	return parsed.String(), nil
}
