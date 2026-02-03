//go:build windows
// +build windows

package main

import "syscall"

func setSockLinger(fd uintptr) error {
	linger := syscall.Linger{Onoff: 1, Linger: 0}
	return syscall.SetsockoptLinger(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_LINGER, &linger)
}
