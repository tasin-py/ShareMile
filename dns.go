package main

import (
	"context"
	"errors"
	"net"
	"time"
)

// Public DNS fallback. A brand-new relay address (xyz.trycloudflare.com) takes
// a few seconds to exist; if anyone looks it up too early, many ISP resolvers
// remember "does not exist" for minutes. ShareMile therefore (1) publishes a
// relay link only once public DNS knows it, and (2) falls back to Cloudflare /
// Google DNS when the system resolver can't find a host.

var publicResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		c, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
		if err != nil {
			c, err = d.DialContext(ctx, "udp", "8.8.8.8:53")
		}
		return c, err
	},
}

// dialWithDNSFallback dials normally and, if the name can't be resolved,
// resolves it through public DNS and dials the address directly.
func dialWithDNSFallback(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := base.DialContext(ctx, network, addr)
		var dnsErr *net.DNSError
		if err == nil || !errors.As(err, &dnsErr) {
			return c, err
		}
		host, port, perr := net.SplitHostPort(addr)
		if perr != nil {
			return nil, err
		}
		ips, rerr := publicResolver.LookupIPAddr(ctx, host)
		if rerr != nil || len(ips) == 0 {
			return nil, err
		}
		var last error = err
		for _, ip := range ips {
			if c, e := base.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port)); e == nil {
				return c, nil
			} else {
				last = e
			}
		}
		return nil, last
	}
}

// waitPublicDNS waits until public DNS can resolve host (max d).
func waitPublicDNS(host string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ips, err := publicResolver.LookupIPAddr(ctx, host)
		cancel()
		if err == nil && len(ips) > 0 {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}
