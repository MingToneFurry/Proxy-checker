//go:build windows
// +build windows

package main

// detectFDLimit returns a conservative default on Windows.
func detectFDLimit() uint64 {
	return 8192
}
