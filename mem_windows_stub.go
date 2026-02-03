//go:build !windows
// +build !windows

package main

// windowsMemLimit reports 0 on non-Windows platforms.
func windowsMemLimit() int64 {
	return 0
}
