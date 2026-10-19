//go:build windows

package main

import "errors"

func dropNetworkUser() error {
	return errors.New("device privilege drop is supported only on Linux")
}
