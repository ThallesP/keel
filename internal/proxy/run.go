//go:build linux

package proxy

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"

	// Only the modules the generated config uses (proxy-ingress.md §12.2). Not the caddy-l4 root
	// package: it imports every l4 module.
	_ "github.com/caddyserver/caddy/v2/modules/caddyevents"            // events app (cert_obtained / cert_failed)
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"              // http app, servers, host matcher, automatic HTTPS
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy" // http.handlers.reverse_proxy
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"               // internal issuer for localhost names (dev)
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"               // tls app, ACME issuers, cert cache
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"            // caddy.storage.file_system
	_ "github.com/mholt/caddy-l4/layer4"                               // layer4 app
	_ "github.com/mholt/caddy-l4/modules/l4proxy"                      // layer4.handlers.proxy
)

// DefaultSocket is where the admin API listens unless KEEL_PROXY_SOCKET says otherwise. The
// control plane mounts the same directory (adapters/caddy).
const DefaultSocket = "/run/keel-proxy/admin.sock"

// baseConfig is caddy.json (was apps/proxy/caddy.json): the admin endpoint only. The control plane then POSTs
// /config/apps; nothing else is ever configured here.
//
//go:embed caddy.json
var baseConfig []byte

// Options configure `keel proxy`.
type Options struct {
	// Socket is the admin socket ("" = DefaultSocket).
	Socket string
	// ConfigFile replaces the built-in base config ("" = built in).
	ConfigFile string
	// Resume serves the last config the control plane pushed (Caddy's autosave, under
	// $XDG_CONFIG_HOME/caddy) when there is one, as `caddy run --resume` did.
	Resume bool
}

// Run serves until ctx is done. Certificates and ACME accounts live under $XDG_DATA_HOME/caddy,
// the autosave under $XDG_CONFIG_HOME/caddy (the keel-proxy image set /data and /config).
func Run(ctx context.Context, opts Options) error {
	prepareACME()
	config, resumed, err := initialConfig(opts)
	if err != nil {
		return err
	}
	socket := opts.Socket
	if socket == "" {
		socket = DefaultSocket
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	log := caddy.Log()
	if resumed {
		log.Info("resuming from last configuration", zap.String("autosave_file", caddy.ConfigAutosavePath))
	}
	if err := caddy.Load(config, true); err != nil {
		if !resumed {
			return fmt.Errorf("loading initial config: %w", err)
		}
		// The last pushed config no longer loads here: a host address went away (DHCP), or
		// another process took an exposed port. Exiting would crash-loop with the admin socket
		// down for good, so the control plane could never push one that loads. Serve the admin
		// endpoint alone instead (it replaces the autosave); the next sync rebuilds from the
		// current host addresses and blames the listener that cannot bind.
		log.Error("the last configuration no longer loads; starting with the admin endpoint only", zap.Error(err))
		if config, err = startConfig(opts); err != nil {
			return err
		}
		if err := caddy.Load(config, true); err != nil {
			return fmt.Errorf("loading initial config: %w", err)
		}
	}
	log.Info("keel proxy serving", zap.String("admin", socket))
	<-ctx.Done()
	return caddy.Stop()
}

// prepareACME does what Caddy's command does at start (caddy/cmd init): name the client to CAs
// and accept their terms, without which ACME account creation fails.
func prepareACME() {
	version, _ := caddy.Version()
	certmagic.UserAgent = "Caddy/" + strings.TrimPrefix(version, "v")
	if ua, ok := os.LookupEnv("USERAGENT"); ok {
		certmagic.UserAgent = ua + " " + certmagic.UserAgent
	}
	certmagic.DefaultACME.Agreed = true
}

// initialConfig: the autosave when resuming and it exists, else startConfig.
func initialConfig(opts Options) (config []byte, resumed bool, err error) {
	if opts.Resume {
		config, err = os.ReadFile(caddy.ConfigAutosavePath)
		if err == nil {
			return config, true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, false, err
		}
	}
	config, err = startConfig(opts)
	return config, false, err
}

// startConfig: opts.ConfigFile, else the base config with the admin socket at opts.Socket.
func startConfig(opts Options) ([]byte, error) {
	if opts.ConfigFile != "" {
		return os.ReadFile(opts.ConfigFile)
	}
	return baseWithSocket(opts.Socket)
}

// baseWithSocket is the built-in base config with the admin endpoint on socket (mode 0600).
func baseWithSocket(socket string) ([]byte, error) {
	if socket == "" {
		return baseConfig, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(baseConfig, &cfg); err != nil {
		return nil, err
	}
	cfg["admin"] = map[string]any{"listen": "unix/" + socket + "|0600"}
	return json.Marshal(cfg)
}
