//go:build windows
// +build windows

package main

import (
	"syscall"
	"unsafe"
)

// windowsMemLimit returns the physical memory size when running on Windows.
// Uses kernel32 APIs directly to avoid relying on newer x/sys symbols.
func windowsMemLimit() int64 {
	// Try GlobalMemoryStatusEx first.
	type memoryStatusEx struct {
		cbSize                  uint32
		dwMemoryLoad            uint32
		ullTotalPhys            uint64
		ullAvailPhys            uint64
		ullTotalPageFile        uint64
		ullAvailPageFile        uint64
		ullTotalVirtual         uint64
		ullAvailVirtual         uint64
		ullAvailExtendedVirtual uint64
	}

	var m memoryStatusEx
	m.cbSize = uint32(unsafe.Sizeof(m))
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	gmse := kernel32.NewProc("GlobalMemoryStatusEx")
	if gmse.Find() == nil {
		r1, _, _ := gmse.Call(uintptr(unsafe.Pointer(&m)))
		if r1 != 0 && m.ullTotalPhys > 0 {
			return int64(m.ullTotalPhys)
		}
	}

	// Fallback: GetPhysicallyInstalledSystemMemory (returns kilobytes).
	gpis := kernel32.NewProc("GetPhysicallyInstalledSystemMemory")
	if gpis.Find() == nil {
		var totalKB uint64
		r1, _, _ := gpis.Call(uintptr(unsafe.Pointer(&totalKB)))
		if r1 != 0 && totalKB > 0 {
			return int64(totalKB) * 1024
		}
	}

	return 0
}
