//go:build linux

package proxy

import (
	"net"
	"testing"
)

func TestPublic(t *testing.T) {
	cases := []struct {
		iface, ip string
		want      bool
	}{
		{"enp1s0", "192.168.0.197", true},
		{"eth0", "203.0.113.7", true},
		{"eth0", "2001:db8::1", true},
		{"eth0", "fe80::1", false},
		{"lo", "127.0.0.1", false},
		{"tailscale0", "100.123.155.61", false},
		{"eth0", "100.100.1.1", false},
		{"eth0", "fd7a:115c:a1e0::1", false},
		{"docker_gwbridge", "172.19.0.1", false},
		{"br-7f74391f29c5", "172.22.0.1", false},
	}
	for _, c := range cases {
		if got := public(c.iface, net.ParseIP(c.ip)); got != c.want {
			t.Errorf("public(%s, %s) = %v, want %v", c.iface, c.ip, got, c.want)
		}
	}
}
