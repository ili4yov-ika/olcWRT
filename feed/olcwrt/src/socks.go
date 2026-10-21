package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// socksDialer opens TCP connections through the local olcrtc SOCKS5 listener.
type socksDialer struct {
	addr string
	user string
	pass string
}

var (
	errSocksAuth  = errors.New("socks5 authentication failed")
	errSocksReply = errors.New("socks5 connect failed")
)

// DialContext connects to target ("host:port") through the proxy. The host
// is sent as-is, so domain names are resolved on the olcrtc server side.
func (d *socksDialer) DialContext(ctx context.Context, network, target string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port in %q", target)
	}
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, err
	}
	// The handshake waits until olcrtc has a live session; bound it by ctx.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	err = d.handshake(conn, host, uint16(port))
	if !stop() || err != nil {
		conn.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	return conn, nil
}

func (d *socksDialer) handshake(conn net.Conn, host string, port uint16) error {
	method := byte(0x00)
	if d.user != "" {
		method = 0x02
	}
	if _, err := conn.Write([]byte{5, 1, method}); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return err
	}
	if resp[0] != 5 {
		return fmt.Errorf("unexpected socks version %d", resp[0])
	}
	switch resp[1] {
	case 0x00:
	case 0x02:
		if err := d.authenticate(conn); err != nil {
			return err
		}
	default:
		return errSocksAuth
	}

	req := []byte{5, 1, 0}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.Is4() {
			a := addr.As4()
			req = append(append(req, 1), a[:]...)
		} else {
			a := addr.As16()
			req = append(append(req, 4), a[:]...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("host name too long")
		}
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = binary.BigEndian.AppendUint16(req, port)
	if _, err := conn.Write(req); err != nil {
		return err
	}
	return readSocksReply(conn)
}

func (d *socksDialer) authenticate(conn net.Conn) error {
	if d.user == "" || len(d.user) > 255 || len(d.pass) > 255 {
		return errSocksAuth
	}
	msg := []byte{1, byte(len(d.user))}
	msg = append(msg, d.user...)
	msg = append(msg, byte(len(d.pass)))
	msg = append(msg, d.pass...)
	if _, err := conn.Write(msg); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return err
	}
	if resp[1] != 0 {
		return errSocksAuth
	}
	return nil
}

func readSocksReply(conn net.Conn) error {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return err
	}
	if hdr[1] != 0 {
		return fmt.Errorf("%w: code %d", errSocksReply, hdr[1])
	}
	var skip int
	switch hdr[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return err
		}
		skip = int(l[0])
	default:
		return fmt.Errorf("%w: bad address type %d", errSocksReply, hdr[3])
	}
	_, err := io.ReadFull(conn, make([]byte, skip+2))
	return err
}
