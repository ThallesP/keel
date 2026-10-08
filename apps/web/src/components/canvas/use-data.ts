import { useMemo } from "react";

import { useGetEnvironmentSummary, useGetLatestDeployment } from "@/api/gen";
import type { EnvironmentSummary } from "@/api/types";

import { useEnvironment } from "./environment";
import { toDeployment } from "./mapping";
import type { Deployment } from "./types";

/**
 * Ship-button + status-bar numbers for the current environment (undefined while loading, null
 * when the environment is not the caller's).
 */
export function useSummary(): EnvironmentSummary | null | undefined {
  const { environmentId } = useEnvironment();
  const { data } = useGetEnvironmentSummary({ path: { id: environmentId } });
  return data?.summary;
}

/** Most recent deployment (undefined while loading, null when none). */
export function useLatestDeployment(): Deployment | null | undefined {
  const { environmentId } = useEnvironment();
  const { data } = useGetLatestDeployment({ path: { id: environmentId } });
  const doc = data?.deployment;
  return useMemo(() => (doc ? toDeployment(doc) : doc), [doc]);
}
