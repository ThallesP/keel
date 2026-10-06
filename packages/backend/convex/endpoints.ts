import { ConvexError } from "convex/values";

import type { Doc } from "./_generated/dataModel";
import type { Endpoint } from "./schema";

// Public endpoints served by keel-proxy (docs/networking.md). Shared by nodes.ts (expose),
// proxy*.ts (sync, reports), migrations.ts and the canvas view.

/** Per node; a game server with a few UDP ports fits, a port range does not. */
export const MAX_ENDPOINTS = 10;

/** The proxy's HTTP listeners; tcp endpoints cannot take them. */
export const HTTP_PORTS = new Set([80, 443]);

/** Where tcp/udp endpoints land when their own port is taken. */
const FIRST_SPARE_PORT = 20000;

/**
 * The control plane's public IPv4, detected by install.sh (KEEL_PUBLIC_IP). Names the default
 * domains and the address of tcp/udp endpoints; undefined on installs that predate it.
 */
export function publicIp(): string | undefined {
  return process.env.KEEL_PUBLIC_IP || undefined;
}

export function requirePublicIp() {
  const ip = publicIp();
  if (!ip) {
    throw new ConvexError(
      "Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP",
    );
  }
  return ip;
}

/**
 * `api-x7k2qd.203-0-113-7.sslip.io`: sslip.io resolves it to the IP inside, so HTTPS works with no
 * DNS setup. The suffix (a hash of the node id: Convex ids share their tail) keeps two projects'
 * `api` apart; renaming the node keeps the domain.
 */
export function defaultDomain(node: Pick<Doc<"nodes">, "_id" | "name">, ip: string) {
  return `${node.name}-${shortHash(node._id)}.${ip.replaceAll(".", "-")}.sslip.io`;
}

/** 6 base-36 characters of FNV-1a. Stable, not secret. */
function shortHash(s: string) {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193);
  return (h >>> 0).toString(36).padStart(6, "0").slice(-6);
}

const LABEL_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

export function validDomain(raw: string) {
  const domain = raw.trim().toLowerCase().replace(/\.$/, "");
  const labels = domain.split(".");
  if (domain.length > 253 || labels.length < 2 || !labels.every((l) => LABEL_RE.test(l))) {
    throw new ConvexError("Domain must look like app.example.com");
  }
  return domain;
}

/** Identity of an endpoint across the install: what the proxy listens for. */
export function endpointKey(e: Pick<Endpoint, "protocol" | "domain" | "publicPort">) {
  return e.protocol === "http" ? `http:${e.domain}` : `${e.protocol}:${e.publicPort}`;
}

/** `https://<domain>` or `<ip>:<port>`, what a user pastes into a browser or a client. */
export function endpointAddress(e: Endpoint, ip = publicIp()) {
  if (e.protocol === "http") return `https://${e.domain}`;
  return `${ip ?? "<public IP>"}:${e.publicPort}`;
}

/** An endpoint as the canvas and the CLI see it. */
export function endpointView(e: Endpoint) {
  return {
    protocol: e.protocol,
    port: e.port,
    domain: e.domain,
    publicPort: e.publicPort,
    address: endpointAddress(e),
    state: e.status.state,
    error: e.status.error,
  };
}

/**
 * The container port when nothing else holds it on that protocol, else the first spare one.
 * `taken` includes 80 and 443 for tcp.
 */
export function allocatePublicPort(port: number, taken: Set<number>) {
  if (!taken.has(port)) return port;
  for (let p = FIRST_SPARE_PORT; p <= 65535; p++) if (!taken.has(p)) return p;
  throw new ConvexError("No free public port left");
}

/**
 * What to do about a failed certificate, from Caddy's nested ACME error. The CA's problem type
 * says whose move it is: `connection` (it could not reach 80/443), `dns` (the name does not
 * resolve here), anything else verbatim (rate limits, CAA).
 */
export function certHint(error: string | undefined, ip = publicIp()) {
  if (!error) return "Could not get a certificate";
  const acme = /urn:ietf:params:acme:error:(\w+) - (.+)$/.exec(error);
  // "203.0.113.7: Fetching http://<name>/.well-known/acme-challenge/<token>: Timeout during …"
  const detail = (acme?.[2] ?? error)
    .replace(/Fetching \S+: /, "")
    .replace(/\s*\(ca=[^)]*\)\s*$/, "")
    .replace(/\s*\(likely firewall problem\)/, "")
    .slice(0, 200);
  switch (acme?.[1]) {
    case "connection":
    case "unauthorized":
    case "tls":
      return `Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (${detail})`;
    case "dns":
      return `${detail}. Point the domain at ${ip ?? "the control plane's public IP"} with an A record.`;
    default:
      return `Could not get a certificate: ${detail}`;
  }
}
