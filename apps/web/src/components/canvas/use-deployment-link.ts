import { getRouteApi } from "@tanstack/react-router";
import { useCallback, useMemo } from "react";

import { useGetDeployment } from "@/gen/api";

import { toDeployment } from "./mapping";
import type { Deployment } from "./types";

const route = getRouteApi("/_auth/p/$projectId");

/**
 * `?deployment=<id>` is the selected deployment. Ship, node actions, the status bar and the
 * Deployments tab rows all set it; the panel opens the Deployments tab of an affected node and
 * highlights it. Reload lands on the same view.
 */
export function useDeploymentLink() {
  const { deployment: deploymentId = null } = route.useSearch();
  const navigate = route.useNavigate();

  const open = useCallback(
    (id: string) => void navigate({ search: (prev) => ({ ...prev, deployment: id }) }),
    [navigate],
  );
  const clear = useCallback(
    () => void navigate({ search: (prev) => ({ ...prev, deployment: undefined }), replace: true }),
    [navigate],
  );

  return useMemo(() => ({ deploymentId, open, clear }), [deploymentId, open, clear]);
}

/** The deployment named in the URL: undefined while loading, null when missing or not owned. */
export function useLinkedDeployment(id: string | null): Deployment | null | undefined {
  const { data } = useGetDeployment({ path: { id: id ?? "" } }, { query: { enabled: !!id } });
  // No id: nothing linked (null), whatever an earlier id left in the cache.
  const doc = id ? data?.deployment : null;
  return useMemo(() => (doc ? toDeployment(doc) : doc), [doc]);
}
