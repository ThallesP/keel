import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useQuery } from "convex/react";
import { useMemo } from "react";

import { useEnvironment } from "./environment";
import { toDeployment } from "./mapping";
import type { Deployment } from "./types";

/** Ship-button + status-bar numbers for the current environment. */
export function useSummary() {
  const { environmentId } = useEnvironment();
  return useQuery(api.environments.summary, { environmentId });
}

/** Most recent deployment (undefined while loading, null when none). */
export function useLatestDeployment(): Deployment | null | undefined {
  const { environmentId } = useEnvironment();
  const doc = useQuery(api.deployments.latest, { environmentId });
  return useMemo(() => (doc ? toDeployment(doc) : doc), [doc]);
}
