//go:build unix
// +build unix

package main

import "golang.org/x/sys/unix"

func detectFDLimit() uint64 {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err == nil && lim.Cur > 0 {
		return uint64(lim.Cur)
	}
	return 8192
}
