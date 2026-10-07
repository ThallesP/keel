import { cronJobs } from "convex/server";

import { internal } from "./_generated/api";

const crons = cronJobs();

// keel-proxy (docs/networking.md "Sync and status"): retry failed endpoints, follow address changes.
crons.interval("keel-proxy resync", { minutes: 2 }, internal.proxyInternal.resync, {});

export default crons;
