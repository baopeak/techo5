//go:build linux

package main

import (
	"errors"
	"os"
	"syscall"
)

// Keep crown's network group and the narrow app-control socket group,
// but drop root and all root capabilities.
func dropNetworkUser() error {
	if os.Geteuid() != 0 {
		return errors.New("privilege drop must begin in the root supervisor")
	}
	if err := syscall.Setgroups([]int{3003, 65532}); err != nil {
		return err
	}
	if err := syscall.Setgid(65534); err != nil {
		return err
	}
	return syscall.Setuid(65534)
}
