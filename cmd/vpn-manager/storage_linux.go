package main

import (
	"path/filepath"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"golang.org/x/sys/unix"
)

func storageVolumes(dataDir string) []api.Storage {
	result := []api.Storage{}
	seen := make(map[uint64]bool)
	paths := []string{dataDir, "/"}
	var overlay, data, root unix.Stat_t
	if unix.Stat("/overlay", &overlay) == nil && unix.Stat("/", &root) == nil && overlay.Dev != root.Dev {
		paths = []string{"/overlay"}
		if unix.Stat(dataDir, &data) == nil && data.Dev != root.Dev {
			paths = append(paths, dataDir)
		}
	}
	for _, path := range paths {
		var info unix.Stat_t
		if unix.Stat(path, &info) != nil || seen[info.Dev] {
			continue
		}
		var space unix.Statfs_t
		if unix.Statfs(path, &space) != nil {
			continue
		}
		seen[info.Dev] = true
		label := "Данные VPN Manager"
		if path == "/overlay" {
			label = "OpenWrt overlay"
		}
		if path == "/" {
			label = "Системный раздел"
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = path
		}
		block := uint64(space.Bsize)
		if space.Frsize > 0 {
			block = uint64(space.Frsize)
		}
		result = append(result, api.Storage{Name: label, Path: absolute, Total: int(space.Blocks * block), Free: int(space.Bfree * block), Available: int(space.Bavail * block)})
	}
	return result
}

func platformUsage() (total, idle uint64, used, memory int) {
	// Linux counters are sampled from /proc.
	return
}
