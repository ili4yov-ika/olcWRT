//go:build linux

package main

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"syscall"
	"unsafe"
)

const (
	soOriginalDst     = 80 // SO_ORIGINAL_DST, linux/netfilter_ipv4.h
	ip6tSoOriginalDst = 80 // IP6T_SO_ORIGINAL_DST, linux/netfilter_ipv6/ip6_tables.h
)

// childSysProcAttr makes the kernel stop olcrtc if the daemon dies.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

// detachedSysProcAttr starts a process in its own session so it outlives
// the short-lived rpcd plugin call that spawned it.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// lockFile takes an exclusive advisory lock and returns its release function.
func lockFile(path string) (func(), error) {
	unlock, _, err := flockPath(path, syscall.LOCK_EX)
	return unlock, err
}

// tryLockFile is the non-blocking variant; ok is false when another process
// holds the lock.
func tryLockFile(path string) (func(), bool, error) {
	return flockPath(path, syscall.LOCK_EX|syscall.LOCK_NB)
}

func flockPath(path string, how int) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true, nil
}

// originalDst returns the pre-REDIRECT destination of a connection.
func originalDst(c *net.TCPConn) (netip.AddrPort, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var dst netip.AddrPort
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		local, _ := c.LocalAddr().(*net.TCPAddr)
		if local != nil && local.IP.To4() == nil {
			dst, sockErr = originalDst6(int(fd))
			if sockErr == nil {
				return
			}
		}
		dst, sockErr = originalDst4(int(fd))
	})
	if ctrlErr != nil {
		return netip.AddrPort{}, ctrlErr
	}
	return dst, sockErr
}

func originalDst4(fd int) (netip.AddrPort, error) {
	// struct sockaddr_in fits into the 20 bytes of IPv6Mreq.
	mreq, err := syscall.GetsockoptIPv6Mreq(fd, syscall.SOL_IP, soOriginalDst)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("SO_ORIGINAL_DST: %w", err)
	}
	b := mreq.Multiaddr
	port := uint16(b[2])<<8 | uint16(b[3])
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}), port), nil
}

func originalDst6(fd int) (netip.AddrPort, error) {
	// struct sockaddr_in6 is the head of IPv6MTUInfo.
	info, err := syscall.GetsockoptIPv6MTUInfo(fd, syscall.SOL_IPV6, ip6tSoOriginalDst)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("IP6T_SO_ORIGINAL_DST: %w", err)
	}
	p := (*[2]byte)(unsafe.Pointer(&info.Addr.Port))
	port := uint16(p[0])<<8 | uint16(p[1])
	addr := netip.AddrFrom16(info.Addr.Addr).Unmap()
	return netip.AddrPortFrom(addr, port), nil
}
