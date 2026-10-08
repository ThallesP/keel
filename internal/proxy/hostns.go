package proxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"golang.org/x/sys/unix"
)

// The proxy runs in a container on the Swarm overlay, so it reaches `svc-<id>:<port>` by overlay
// DNS, but its public listeners live in the host's network namespace: a Docker port mapping is
// fixed when the container is created, while a socket opened in the host namespace is not, so
// the control plane adds a TCP or UDP port through the admin API without restarting anything. Needs
// CAP_SYS_ADMIN (setns) and the host's namespace file mounted at HostNetNS (deploy/compose.yml).
const HostNetNS = "/run/hostns/net"

func init() {
	caddy.RegisterNetwork("host-tcp", listenInHost("tcp"))
	caddy.RegisterNetwork("host-udp", listenInHost("udp"))
}

// listenInHost opens a plain tcp/udp listener in the host namespace through Caddy's own listen
// path, so the socket gets SO_REUSEPORT and Caddy's listener pool: a config reload re-binds the
// same address while the old listener still serves, and open connections survive it.
func listenInHost(network string) caddy.ListenerFunc {
	return func(ctx context.Context, _, host, portRange string, portOffset uint, cfg net.ListenConfig) (any, error) {
		port, err := strconv.ParseUint(portRange, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("host networks take a single port, got %q", portRange)
		}
		addr := caddy.NetworkAddress{Network: network, Host: host, StartPort: uint(port), EndPort: uint(port)}
		var ln any
		err = inHost(func() error {
			var err error
			ln, err = addr.Listen(ctx, portOffset, cfg)
			return err
		})
		return ln, err
	}
}

// inHost runs fn on an OS thread switched into the host's network namespace. A socket keeps the
// namespace it was created in, so what fn opens stays on the host after the thread switches back.
func inHost(fn func() error) error {
	runtime.LockOSThread()
	own, err := os.Open(fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid()))
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer own.Close()
	host, err := os.Open(HostNetNS)
	if err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("host network namespace: %w", err)
	}
	defer host.Close()
	if err := unix.Setns(int(host.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("enter host network namespace: %w", err)
	}
	defer func() {
		// A thread that cannot switch back stays locked, so Go retires it with this goroutine.
		if unix.Setns(int(own.Fd()), unix.CLONE_NEWNET) == nil {
			runtime.UnlockOSThread()
		}
	}()
	return fn()
}

// hostAddrs lists the host addresses the proxy binds: every address of every up interface except
// loopback, Tailscale and Docker's own. Never a wildcard: `tailscale serve` (the dashboard over
// HTTPS) holds 443 on the tailnet address, and 0.0.0.0:443 cannot bind next to it.
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
	tailnetV4 = mustCIDR("100.64.0.0/10")
	tailnetV6 = mustCIDR("fd7a:115c:a1e0::/48")
	// Interfaces Docker and Tailscale create; their addresses are never where public traffic lands.
	skipPrefixes = []string{"lo", "tailscale", "docker", "br-", "veth"}
)

func public(iface string, ip net.IP) bool {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(iface, p) {
			return false
		}
	}
	return !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() &&
		!tailnetV4.Contains(ip) && !tailnetV6.Contains(ip)
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}
