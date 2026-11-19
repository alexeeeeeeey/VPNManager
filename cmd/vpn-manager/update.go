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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"golang.org/x/mod/semver"
)

const releaseRepo = "alexeeeeeeey/VPNManager"
const maximumArchive = 32 << 20
const maximumBinary = 32 << 20

type panelUpdater struct {
	executable string
	restart    context.CancelFunc
	checks     sync.Mutex
	installing sync.Mutex
	cached     api.PanelUpdate
	expires    time.Time
}

var releaseClient = &http.Client{
	Timeout: 50 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" {
			return errors.New("неожиданное перенаправление релиза")
		}
		switch req.URL.Host {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return errors.New("неожиданное перенаправление релиза")
	},
}

func releaseVersion(value string) string {
	value = "v" + strings.TrimPrefix(value, "v")
	if !semver.IsValid(value) || semver.Canonical(value) != value || semver.Prerelease(value) != "" {
		return ""
	}
	return strings.TrimPrefix(value, "v")
}

func (u *panelUpdater) status(ctx context.Context) api.PanelUpdate {
	result := api.PanelUpdate{Current: buildVersion}
	if u.executable == "" || releaseVersion(buildVersion) == "" {
		return result
	}
	u.checks.Lock()
	defer u.checks.Unlock()
	if time.Now().Before(u.expires) {
		return u.cached
	}
	result = u.lookup(ctx)
	u.cached = result
	u.expires = time.Now().Add(10 * time.Minute)
	if result.Latest == "" {
		u.expires = time.Now().Add(time.Minute)
	}
	return result
}

func (u *panelUpdater) lookup(ctx context.Context) api.PanelUpdate {
	result := api.PanelUpdate{Current: buildVersion}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := getRelease(ctx, "https://api.github.com/repos/"+releaseRepo+"/releases/latest")
	if err != nil {
		return result
	}
	defer body.Close()
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct{ Name string }
	}
	if json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&release) != nil {
		return result
	}
	latest := releaseVersion(release.Tag)
	if latest == "" {
		return result
	}
	result.Latest = latest
	names := map[string]bool{}
	for _, asset := range release.Assets {
		names[asset.Name] = true
	}
	current := releaseVersion(buildVersion)
	result.Available = semver.Compare("v"+latest, "v"+current) > 0 && names[archiveName(latest)] && names["checksums.txt"]
	return result
}

func archiveName(version string) string {
	return "vpn-manager_" + version + "_linux_" + runtime.GOARCH + ".tar.gz"
}

func getRelease(ctx context.Context, address string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "VPN-Manager")
	response, err := releaseClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GitHub вернул HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}

func expectedChecksum(ctx context.Context, root, name string) ([]byte, error) {
	body, err := getRelease(ctx, root+"checksums.txt")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 128<<10))
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			checksum, err := hex.DecodeString(fields[0])
			if err == nil && len(checksum) == sha256.Size {
				return checksum, nil
			}
		}
	}
	return nil, errors.New("в релизе нет контрольной суммы для этой архитектуры")
}

func extractExecutable(source io.Reader, target io.Writer) error {
	compressed, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(io.LimitReader(compressed, maximumBinary*2))
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return errors.New("в архиве нет vpn-manager")
		}
		if err != nil {
			return err
		}
		if strings.TrimPrefix(header.Name, "./") != "vpn-manager" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maximumBinary {
			return errors.New("некорректный бинарник в архиве")
		}
		_, err = io.CopyN(target, archive, header.Size)
		return err
	}
}

func (u *panelUpdater) install(ctx context.Context, version string) error {
	name := archiveName(version)
	root := "https://github.com/" + releaseRepo + "/releases/download/v" + version + "/"
	expected, err := expectedChecksum(ctx, root, name)
	if err != nil {
		return err
	}
	body, err := getRelease(ctx, root+name)
	if err != nil {
		return err
	}
	defer body.Close()
	archive, err := os.CreateTemp("", "vpn-manager-release-*.tar.gz")
	if err != nil {
		return err
	}
	defer archive.Close()
	defer os.Remove(archive.Name())
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(archive, digest), io.LimitReader(body, maximumArchive+1))
	if err != nil {
		return err
	}
	if size > maximumArchive || !bytes.Equal(digest.Sum(nil), expected) {
		return errors.New("контрольная сумма архива не совпала")
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return err
	}

	dest := u.executable
	info, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("путь панели изменился; установка остановлена")
	}
	staged, err := os.CreateTemp(filepath.Dir(dest), ".vpn-manager-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	err = extractExecutable(archive, staged)
	if err == nil {
		err = staged.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = staged.Sync()
	}
	if err = errors.Join(err, staged.Close()); err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	output, err := exec.CommandContext(probe, staged.Name(), "-version").Output()
	cancel()
	if err != nil || releaseVersion(strings.TrimSpace(string(output))) != version {
		return errors.New("бинарник не прошёл проверку версии")
	}
	if err = preserveExecutable(dest, info.Mode().Perm()); err != nil {
		return fmt.Errorf("не удалось сохранить предыдущую версию: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), dest)
}

func preserveExecutable(path string, mode os.FileMode) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	copyFile, err := os.CreateTemp(filepath.Dir(path), ".vpn-manager-previous-*")
	if err != nil {
		return err
	}
	defer os.Remove(copyFile.Name())
	_, err = io.Copy(copyFile, source)
	if err == nil {
		err = copyFile.Chmod(mode)
	}
	if err == nil {
		err = copyFile.Sync()
	}
	if err = errors.Join(err, copyFile.Close()); err != nil {
		return err
	}
	return os.Rename(copyFile.Name(), path+".previous")
}

func (s server) GetPanelUpdate(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, s.updater.status(r.Context()), nil)
}

func (s server) InstallPanelUpdate(w http.ResponseWriter, r *http.Request) {
	var input api.InstallUpdate
	if !decode(w, r, &input) {
		return
	}
	u := &s.updater
	if !u.installing.TryLock() {
		jsonResponse(w, nil, errors.New("установка уже выполняется"))
		return
	}
	defer u.installing.Unlock()
	update := u.status(r.Context())
	if !update.Available || input.Version != update.Latest {
		jsonResponse(w, nil, errors.New("совместимое обновление недоступно"))
		return
	}
	if err := u.install(r.Context(), update.Latest); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	jsonResponse(w, api.InstallUpdate{Version: update.Latest}, nil)
	u.restart()
}
