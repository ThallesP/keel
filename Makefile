# Keel: one Go binary (cmd/keel) with the dashboard (apps/web) embedded. See docs/go/ARCHITECTURE.md.
#
#   make dev        keel serve on 127.0.0.1:3400 with its state in ./.keel. In another terminal run
#                   the dashboard with `bun run dev:web` (Vite on http://127.0.0.1:3001); Vite
#                   proxies /api (and the /api/ws WebSocket), /worker, /otlp and /proxy to
#                   127.0.0.1:3400, so dashboard, API and socket share one origin as in production.
#                   Set KEEL_SITE_URL to the URL you open (e.g. your ts.net HTTPS name).
#   make test       go vet, go test, gofmt and no-comments checks
#   make openapi    regenerate openapi.json (the dashboard's API client is generated from it)
#   make web        build apps/web/dist
#   make build      bin/keel with the dashboard embedded (-tags embedweb)
#   make image      the release image (root Dockerfile)

GO      ?= go
GOFMT   ?= gofmt
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ThallesP/keel/internal/cli.Version=$(VERSION)

DEV_LISTEN   ?= 127.0.0.1:3400
DEV_DATA     ?= ./.keel
DEV_SITE_URL ?= http://127.0.0.1:3001

.PHONY: dev test fmt openapi web build image clean

dev:
	KEEL_LISTEN=$(DEV_LISTEN) KEEL_DATA_DIR=$(DEV_DATA) KEEL_SITE_URL=$${KEEL_SITE_URL:-$(DEV_SITE_URL)} \
		$(GO) run ./cmd/keel serve

test:
	$(GO) vet ./...
	$(GO) test ./...
	$(GO) run ./tools/nocomments $$($(GO) list -f '{{.Dir}}' ./...)
	@out="$$($(GOFMT) -l cmd internal tools apps/web/*.go)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fmt:
	$(GOFMT) -w cmd internal tools apps/web/*.go

openapi:
	$(GO) run ./cmd/keel openapi > openapi.json

web:
	bun install --frozen-lockfile
	cd apps/web && bun run build

build: web
	CGO_ENABLED=0 $(GO) build -tags embedweb -trimpath -ldflags "$(LDFLAGS)" -o bin/keel ./cmd/keel

image:
	docker build -t keel:$(VERSION) --build-arg VERSION=$(VERSION) .

clean:
	rm -rf bin apps/web/dist
