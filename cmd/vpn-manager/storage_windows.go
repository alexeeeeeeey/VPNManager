package main

import (
	"path/filepath"
	"unsafe"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
	"golang.org/x/sys/windows"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")
var systemTimes = kernel.NewProc("GetSystemTimes")
var memoryStatus = kernel.NewProc("GlobalMemoryStatusEx")

func storageVolumes(dataDir string) []api.Storage {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return []api.Storage{}
	}
	path, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return []api.Storage{}
	}
	var available, total, free uint64
	if windows.GetDiskFreeSpaceEx(path, &available, &total, &free) != nil {
		return []api.Storage{}
	}
	return []api.Storage{{Name: "Том данных приложения", Path: filepath.VolumeName(absolute), Total: int(total), Free: int(free), Available: int(available)}}
}

func platformUsage() (total, idle uint64, used, memory int) {
	var tIdle, tKernel, tUser uint64
	success, _, _ := systemTimes.Call(uintptr(unsafe.Pointer(&tIdle)), uintptr(unsafe.Pointer(&tKernel)), uintptr(unsafe.Pointer(&tUser)))
	if success != 0 {
		total, idle = tKernel+tUser, tIdle
	}
	var status struct {
		Size, Load                                                                   uint32
		Total, Free, TotalPage, FreePage, TotalVirtual, FreeVirtual, ExtendedVirtual uint64
	}
	status.Size = uint32(unsafe.Sizeof(status))
	success, _, _ = memoryStatus.Call(uintptr(unsafe.Pointer(&status)))
	if success != 0 {
		memory, used = int(status.Total), int(status.Total-status.Free)
	}
	return
}
