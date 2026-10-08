package serve

import (
	"encoding/json"

	"github.com/ThallesP/keel/internal/app"
)

// runtimeConfig is the object /config.js assigns to window.__KEEL__ (cli-install.md B3). The CLI
// discovers an install by reading the JSON between the first "{" and the last "}" of that file,
// so keep it one flat JSON object on one line. Fields are only ever added.
type runtimeConfig struct {
	// APIURL is the dashboard origin, which is also the API and WebSocket origin. Omitted when
	// KEEL_SITE_URL is unset (dev): the web then uses location.origin.
	APIURL  string `json:"apiUrl,omitempty"`
	Version string `json:"version"`
	// convexUrl / convexSiteUrl are deliberately absent: an old (Convex-protocol) CLI then fails
	// with its clear "has no Convex URLs" error instead of half-working.
}

// ConfigJS is the body of GET /config.js.
func ConfigJS(cfg app.Config) string {
	b, err := json.Marshal(runtimeConfig{APIURL: cfg.SiteURL, Version: cfg.Version})
	if err != nil {
		panic(err) // two strings always marshal
	}
	return "window.__KEEL__ = " + string(b) + ";\n"
}
