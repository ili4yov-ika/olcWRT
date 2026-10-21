package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const fetchLimit = 16 << 20

// Fetch routes.
const (
	routeDirect = "direct"
	routeTunnel = "tunnel"
)

// fetcher downloads subscriptions and lists either directly through the WAN
// or through the olcrtc tunnel.
type fetcher struct {
	direct []string // DNS servers for direct requests
	socks  *socksDialer
}

func newFetcher(cfg *Config) *fetcher {
	user, pass := cfg.SocksAuth()
	return &fetcher{
		direct: directDNSServers(cfg),
		socks:  &socksDialer{addr: cfg.SocksAddr(), user: user, pass: pass},
	}
}

// directDNSServers returns the resolvers used for traffic that must not
// depend on the tunnel: configured ones, the WAN ones, or public fallbacks.
func directDNSServers(cfg *Config) []string {
	var out []string
	for _, s := range cfg.Direct {
		if hp, err := normalizeHostPort(s, 53); err == nil {
			out = append(out, hp)
		}
	}
	if len(out) == 0 {
		out = systemResolvers()
	}
	if len(out) == 0 {
		out = []string{"77.88.8.8:53", "8.8.8.8:53"}
	}
	return out
}

// directNetResolver resolves names through servers, bypassing the local
// dnsmasq which may itself depend on the tunnel.
func directNetResolver(servers []string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			var lastErr error
			for _, s := range servers {
				c, err := d.DialContext(ctx, network, s)
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

func (f *fetcher) client(route string, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if route == routeTunnel {
		tr.DialContext = f.socks.DialContext
	} else {
		d := &net.Dialer{Timeout: 15 * time.Second, Resolver: directNetResolver(f.direct)}
		tr.DialContext = d.DialContext
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

type fetchResult struct {
	Body   string
	Header http.Header
	Route  string
}

// fetch tries the given routes in order and returns the first success.
func (f *fetcher) fetch(ctx context.Context, url string, headers map[string]string, routes ...string) (*fetchResult, error) {
	var errs []string
	for _, route := range routes {
		res, err := f.fetchVia(ctx, url, headers, route)
		if err == nil {
			return res, nil
		}
		errs = append(errs, route+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New(strings.Join(errs, "; "))
}

func (f *fetcher) fetchVia(ctx context.Context, url string, headers map[string]string, route string) (*fetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain, text/markdown, application/octet-stream, */*")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.client(route, 60*time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > fetchLimit {
		return nil, errors.New("response too large")
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil, errors.New("empty response")
	}
	return &fetchResult{Body: string(body), Header: resp.Header, Route: route}, nil
}

func isHTTPURL(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return (strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")) && !strings.ContainsAny(strings.TrimSpace(s), " \t\r\n")
}
