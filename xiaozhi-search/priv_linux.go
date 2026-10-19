//go:build linux

package main

import (
	"errors"
	"os"
	"syscall"
)

// The crown Android-derived kernel requires supplementary group inet (3003)
// for network sockets. Keep that group, but drop root and all root capabilities.
func dropNetworkUser() error {
	if os.Geteuid() != 0 {
		return errors.New("privilege drop must begin in the root supervisor")
	}
	if err := syscall.Setgroups([]int{3003}); err != nil {
		return err
	}
	if err := syscall.Setgid(65534); err != nil {
		return err
	}
	return syscall.Setuid(65534)
}
