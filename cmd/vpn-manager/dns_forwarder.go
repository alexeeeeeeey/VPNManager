package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const panelDNSPort = 1053

func dnsmasqFragment() (string, error) {
	files, err := filepath.Glob("/var/etc/dnsmasq.conf.*")
	if err != nil || len(files) != 1 {
		return "", errors.New("автоматическое подключение DNS требует один экземпляр dnsmasq в OpenWrt")
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "conf-dir="); ok {
			dir, _, _ := strings.Cut(value, ",")
			dir = filepath.Clean(dir)
			if strings.HasPrefix(dir, "/tmp/") {
				return filepath.Join(dir, "vpn-manager-dns.conf"), nil
			}
		}
	}
	return "", errors.New("не найден временный каталог настроек dnsmasq")
}

func (g *dnsGateway) forwardDNS(ctx context.Context, port int) error {
	file, err := dnsmasqFragment()
	if err != nil {
		if port == 0 {
			return nil
		}
		return err
	}
	previous, err := os.ReadFile(file)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Ignore WAN resolvers while preserving dnsmasq's local records and
	// more specific domain-specific upstreams.
	content := "no-resolv\nserver=127.0.0.1#" + strconv.Itoa(port) + "\n"
	if (port == 0 && !exists) || (port > 0 && string(previous) == content) {
		return nil
	}
	write := func(data []byte) error {
		if data == nil {
			if err := os.Remove(file); !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}
		temp, err := os.CreateTemp(filepath.Dir(file), ".vpn-manager-dns-")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		if err = temp.Chmod(0644); err == nil {
			_, err = temp.Write(data)
		}
		if err = errors.Join(err, temp.Close()); err != nil {
			return err
		}
		return os.Rename(temp.Name(), file)
	}
	restart := func(ctx context.Context) error {
		timeout, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		output, err := exec.CommandContext(timeout, "/etc/init.d/dnsmasq", "restart").CombinedOutput()
		if err != nil {
			return fmt.Errorf("перезапуск dnsmasq: %w %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	var next []byte
	if port > 0 {
		next = []byte(content)
	}
	if err := write(next); err != nil {
		return err
	}
	if err := restart(ctx); err != nil {
		if writeErr := write(previous); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return errors.Join(err, restart(context.Background()))
	}
	return nil
}
