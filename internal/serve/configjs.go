package serve

import (
	"encoding/json"

	"github.com/ThallesP/keel/internal/app"
)

type runtimeConfig struct {
	APIURL  string `json:"apiUrl,omitempty"`
	Version string `json:"version"`
}

func ConfigJS(cfg app.Config) string {
	b, err := json.Marshal(runtimeConfig{APIURL: cfg.SiteURL, Version: cfg.Version})
	if err != nil {
		panic(err)
	}
	return "window.__KEEL__ = " + string(b) + ";\n"
}
