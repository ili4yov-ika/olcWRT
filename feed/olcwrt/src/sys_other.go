//go:build !linux

package main

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
)

var errUnsupported = errors.New("not supported on this platform")

func childSysProcAttr() *syscall.SysProcAttr    { return nil }
func detachedSysProcAttr() *syscall.SysProcAttr { return nil }

func lockFile(string) (func(), error) { return func() {}, nil }

func tryLockFile(string) (func(), bool, error) { return func() {}, true, nil }

func originalDst(*net.TCPConn) (netip.AddrPort, error) { return netip.AddrPort{}, errUnsupported }
