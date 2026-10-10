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

	_ "github.com/caddyserver/caddy/v2/modules/caddyevents"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
	_ "github.com/mholt/caddy-l4/layer4"
	_ "github.com/mholt/caddy-l4/modules/l4proxy"
)

const DefaultSocket = "/run/keel-proxy/admin.sock"

//go:embed caddy.json
var baseConfig []byte

type Options struct {
	Socket     string
	ConfigFile string
	Resume     bool
}

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

func prepareACME() {
	version, _ := caddy.Version()
	certmagic.UserAgent = "Caddy/" + strings.TrimPrefix(version, "v")
	if ua, ok := os.LookupEnv("USERAGENT"); ok {
		certmagic.UserAgent = ua + " " + certmagic.UserAgent
	}
	certmagic.DefaultACME.Agreed = true
}

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

func startConfig(opts Options) ([]byte, error) {
	if opts.ConfigFile != "" {
		return os.ReadFile(opts.ConfigFile)
	}
	return baseWithSocket(opts.Socket)
}

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
