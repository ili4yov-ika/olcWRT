package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeSocks is a minimal SOCKS5 server that records requested targets and
// connects them through dial.
type fakeSocks struct {
	ln      net.Listener
	user    string
	pass    string
	dial    func(target string) (net.Conn, error)
	mu      sync.Mutex
	targets []string
}

func newFakeSocks(t *testing.T, user, pass string, dial func(string) (net.Conn, error)) *fakeSocks {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSocks{ln: ln, user: user, pass: pass, dial: dial}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeSocks) addr() string { return s.ln.Addr().String() }

func (s *fakeSocks) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *fakeSocks) serve(c net.Conn) {
	defer c.Close()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	methods := make([]byte, hdr[1])
	io.ReadFull(c, methods)
	if s.user != "" {
		c.Write([]byte{5, 2})
		ver := make([]byte, 2)
		io.ReadFull(c, ver)
		u := make([]byte, ver[1])
		io.ReadFull(c, u)
		pl := make([]byte, 1)
		io.ReadFull(c, pl)
		p := make([]byte, pl[0])
		io.ReadFull(c, p)
		if string(u) != s.user || string(p) != s.pass {
			c.Write([]byte{1, 1})
			return
		}
		c.Write([]byte{1, 0})
	} else {
		c.Write([]byte{5, 0})
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		io.ReadFull(c, l)
		b := make([]byte, l[0])
		io.ReadFull(c, b)
		host = string(b)
	}
	pb := make([]byte, 2)
	io.ReadFull(c, pb)
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()

	up, err := s.dial(target)
	if err != nil {
		c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	done := make(chan struct{})
	go func() { io.Copy(up, c); closeWrite(up); close(done) }()
	io.Copy(c, up)
	closeWrite(c)
	<-done
}

// echoServer echoes everything back.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestSocksDialerTargets(t *testing.T) {
	echo := echoServer(t)
	srv := newFakeSocks(t, "", "", func(string) (net.Conn, error) { return net.Dial("tcp", echo) })
	d := &socksDialer{addr: srv.addr()}
	for _, target := range []string{"93.184.216.34:443", "[2001:db8::1]:80", "example.com:8443"} {
		c, err := d.DialContext(context.Background(), "tcp", target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		c.Write([]byte("ping"))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("%s: echo %q %v", target, buf, err)
		}
		c.Close()
	}
	got := srv.seen()
	want := []string{"93.184.216.34:443", "[2001:db8::1]:80", "example.com:8443"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("targets %v, want %v", got, want)
	}
}

func TestSocksDialerAuthAndFailure(t *testing.T) {
	srv := newFakeSocks(t, "user", "secret", func(string) (net.Conn, error) { return nil, io.EOF })
	if _, err := (&socksDialer{addr: srv.addr(), user: "user", pass: "wrong"}).DialContext(context.Background(), "tcp", "1.2.3.4:80"); err == nil {
		t.Fatal("expected auth failure")
	}
	if _, err := (&socksDialer{addr: srv.addr(), user: "user", pass: "secret"}).DialContext(context.Background(), "tcp", "1.2.3.4:80"); err == nil {
		t.Fatal("expected connect failure")
	}
}

func TestSocksDialerContextCancel(t *testing.T) {
	// A server that accepts but never answers, like olcrtc waiting for a session.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (&socksDialer{addr: ln.Addr().String()}).DialContext(ctx, "tcp", "1.2.3.4:80"); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("dial did not honour the context")
	}
}
