-- Ingress area (docs/go/spec/proxy-ingress.md). Endpoints are written with CoreInsertEndpoint
-- through ReplaceEndpoints; these are the lookups expose, the proxy sync and cert reports need.

-- name: IngressHasVariable :one
SELECT EXISTS (SELECT 1 FROM variables WHERE node_id = ? AND key = ?);

-- name: IngressListOtherEndpoints :many
SELECT e.protocol, e.domain, e.public_port, n.name
FROM endpoints e JOIN nodes n ON n.id = e.node_id
WHERE e.node_id <> ?
ORDER BY n.created_at, n.id, e.ord;

-- name: IngressListRoutes :many
SELECT e.node_id, e.protocol, e.port, e.domain, e.public_port
FROM endpoints e JOIN nodes n ON n.id = e.node_id
ORDER BY n.created_at, n.id, e.ord;

-- name: IngressAnyEndpoint :one
SELECT EXISTS (SELECT 1 FROM endpoints);

-- name: IngressListNodesWithDomain :many
SELECT node_id FROM endpoints WHERE protocol = 'http' AND domain = ?;
