//go:build !linux && !windows

package main

import "github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"

func storageVolumes(string) []api.Storage                   { return []api.Storage{} }
func platformUsage() (total, idle uint64, used, memory int) { return }
