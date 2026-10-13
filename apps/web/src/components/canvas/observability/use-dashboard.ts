import { useQueryClient } from "@tanstack/react-query";

import {
  type Dashboard,
  getDashboardSuspenseQueryKey,
  type Panel,
  useGetDashboardSuspense,
  useSaveDashboard,
} from "@/gen/api";
import { succeeded } from "@/lib/panel-write";

import { useEnvironment } from "../environment";

export function useDashboard() {
  const { environmentId } = useEnvironment();
  const path = { id: environmentId };
  const queryClient = useQueryClient();
  const {
    data: { panels },
  } = useGetDashboardSuspense({ path });
  const { mutateAsync: saveDashboard } = useSaveDashboard();

  const save = async (next: Panel[]) => {
    const queryKey = getDashboardSuspenseQueryKey({ path });
    await queryClient.cancelQueries({ queryKey });
    queryClient.setQueryData<Dashboard>(queryKey, { panels: next });
    const ok = await succeeded(saveDashboard({ path, body: { panels: next } }));
    if (!ok) await queryClient.invalidateQueries({ queryKey });
  };
  return { panels, save };
}
