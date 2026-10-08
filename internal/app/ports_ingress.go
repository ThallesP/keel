package app

// IngressTx: endpoint lookups beyond CoreTx (endpoints are written with ReplaceEndpoints).
// Owner: the ingress area (docs/go/spec/proxy-ingress.md).
type IngressTx interface{}

// Proxy is keel-proxy's admin API over its unix socket (adapters/caddy).
type Proxy interface{}
