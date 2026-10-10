//go:build linux

package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"golang.org/x/sys/unix"
)

func init() {
	caddy.RegisterNetwork("host-tcp", listenInHost("tcp"))
	caddy.RegisterNetwork("host-udp", listenInHost("udp"))
}

func listenInHost(network string) caddy.ListenerFunc {
	return func(ctx context.Context, _, host, portRange string, portOffset uint, cfg net.ListenConfig) (any, error) {
		port, err := strconv.ParseUint(portRange, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("host networks take a single port, got %q", portRange)
		}
		addr := caddy.NetworkAddress{Network: network, Host: host, StartPort: uint(port), EndPort: uint(port)}
		var ln any
		err = inHost(func() (err error) {
			ln, err = addr.Listen(ctx, portOffset, cfg)
			return err
		})
		return ln, err
	}
}

func inHost(fn func() error) error {
	host, err := os.Open("/run/hostns/net")
	if err != nil {
		return fmt.Errorf("host network namespace: %w", err)
	}
	defer host.Close()
	runtime.LockOSThread()
	own, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer own.Close()
	if err := unix.Setns(int(host.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("enter host network namespace: %w", err)
	}
	defer func() {
		if unix.Setns(int(own.Fd()), unix.CLONE_NEWNET) == nil {
			runtime.UnlockOSThread()
		}
	}()
	return fn()
}

func hostAddrs() ([]string, error) {
	var addrs []string
	err := inHost(func() error {
		ifaces, err := net.Interfaces()
		if err != nil {
			return err
		}
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			list, err := iface.Addrs()
			if err != nil {
				return err
			}
			for _, a := range list {
				if ipnet, ok := a.(*net.IPNet); ok && public(iface.Name, ipnet.IP) {
					addrs = append(addrs, ipnet.IP.String())
				}
			}
		}
		return nil
	})
	return addrs, err
}

var (
	tailnetV4    = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6    = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	skipPrefixes = []string{"lo", "tailscale", "docker", "br-", "veth"}
)

func public(iface string, ip net.IP) bool {
	if slices.ContainsFunc(skipPrefixes, func(p string) bool { return strings.HasPrefix(iface, p) }) {
		return false
	}
	addr, _ := netip.AddrFromSlice(ip)
	addr = addr.Unmap()
	return !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && !addr.IsMulticast() &&
		!tailnetV4.Contains(addr) && !tailnetV6.Contains(addr)
}
