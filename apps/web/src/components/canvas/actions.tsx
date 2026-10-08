import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useMemo, useState, type ReactNode } from "react";

import {
  listNodesQueryKey,
  useCreateNode,
  useDeleteNode,
  useDuplicateNode,
  useExposeNode,
  useMoveNode,
  useShipEnvironment,
  useStartNode,
  useStopNode,
  useUnexposeNode,
  useUpdateNode,
} from "@/api/gen";
import type { NodeView, Position } from "@/api/types";
import { CanvasOverlay } from "@/lib/canvas-overlay";

import { useEnvironment } from "./environment";
import { attempt } from "./errors";
import type { Endpoint, InfraNodeType } from "./types";
import { useDeploymentLink } from "./use-deployment-link";

/**
 * Service: an image to run. Database / cache: an engine from the catalog. Omit for the type
 * default. `deploy` ships the node immediately and opens its Deployments tab.
 */
export type CreateOptions = {
  image?: string;
  engine?: "postgres" | "mysql" | "mongo" | "redis";
  deploy?: boolean;
};

export type ExposeOptions = {
  protocol?: "http" | "tcp" | "udp";
  port?: number;
  domain?: string;
  publicPort?: number;
};

export type EndpointRef = Pick<Endpoint, "protocol" | "domain" | "publicPort">;

export type CanvasActions = {
  create: (type: InfraNodeType, position: Position, options?: CreateOptions) => Promise<void>;
  /** Scale to 1 and ship; "Deploy" for never-shipped nodes. */
  start: (id: string) => Promise<void>;
  /** Scale to 0 and ship. */
  stop: (id: string) => Promise<void>;
  /** Ship one node. `refresh` re-pulls the image (Redeploy); without it tasks just restart. */
  redeploy: (id: string, refresh: boolean) => Promise<void>;
  move: (id: string, position: Position) => void;
  rename: (id: string, name: string) => Promise<void>;
  /**
   * Reachable from the internet through keel-proxy; immediate, not a ship. No options: https on a
   * generated domain for a service, tcp on its own port for a database or cache.
   */
  expose: (id: string, options?: ExposeOptions) => Promise<boolean>;
  /** One endpoint, or all of them ("Make private"). */
  unexpose: (id: string, endpoint?: EndpointRef) => Promise<void>;
  duplicate: (id: string) => Promise<void>;
  removeNodes: (ids: string[]) => void;
  /** Ship every dirty node, or `only` these (re-pulling their images). */
  ship: (only?: string[]) => Promise<void>;
  /**
   * Moves and deletes still in flight (re-applied over every node list until they settle), and
   * the ids this client created, which the graph sync selects once the list delivers them.
   */
  overlay: CanvasOverlay;
};

const Context = createContext<CanvasActions | null>(null);

/** `GET /api/environments/{id}/nodes` as cached (`useListNodes`' data). */
type NodeListData = { nodes: NodeView[] | null };

/** Every mutation the canvas fires, scoped to the current environment. Failures become toasts. */
export function CanvasActionsProvider({ children }: { children: ReactNode }) {
  const { environmentId } = useEnvironment();
  const link = useDeploymentLink();
  const queryClient = useQueryClient();
  const [overlay] = useState(() => new CanvasOverlay());

  // `mutateAsync` is stable for the life of each hook.
  const createNode = useCreateNode().mutateAsync;
  const updateNode = useUpdateNode().mutateAsync;
  const duplicateNode = useDuplicateNode().mutateAsync;
  const shipEnvironment = useShipEnvironment().mutateAsync;
  const startNode = useStartNode().mutateAsync;
  const stopNode = useStopNode().mutateAsync;
  const moveNode = useMoveNode().mutateAsync;
  const deleteNode = useDeleteNode().mutateAsync;
  const exposeNode = useExposeNode().mutateAsync;
  const unexposeNode = useUnexposeNode().mutateAsync;

  const value = useMemo<CanvasActions>(() => {
    const env = { id: environmentId };
    const nodesKey = listNodesQueryKey({ path: env });

    // Drag end and delete update the local graph instantly. The overlay keeps the node list in
    // step until the write settles, so a refetch that lands first cannot snap things back; the
    // cache edit shows the change to the node list's other readers (Ship's Retry, Observability)
    // at once. A failed write drops its overlay entry and refetches: the node goes back to where
    // the server has it, and `attempt` toasts why.
    const editNodes = async (edit: (nodes: NodeView[]) => NodeView[]) => {
      await queryClient.cancelQueries({ queryKey: nodesKey });
      queryClient.setQueryData<NodeListData>(
        nodesKey,
        (old) => old && { ...old, nodes: edit(old.nodes ?? []) },
      );
    };
    const settled = async (ok: boolean, settle: () => void) => {
      settle();
      // On success the write's own invalidation already refetched the list (read-your-writes).
      if (!ok) await queryClient.invalidateQueries({ queryKey: nodesKey });
    };

    return {
      create: async (type, position, options) => {
        const result = await attempt(
          createNode({ path: env, body: { type, position, ...options } }),
        );
        if (!result.ok) return;
        overlay.arrive(result.data.id);
        if (result.data.deploymentId) link.open(result.data.deploymentId);
      },
      start: async (id) => {
        const result = await attempt(startNode({ path: { id } }));
        if (result.ok) link.open(result.data.deploymentId);
      },
      stop: async (id) => {
        const result = await attempt(stopNode({ path: { id } }));
        // null: already at 0 replicas, nothing shipped.
        if (result.ok && result.data.deploymentId) link.open(result.data.deploymentId);
      },
      redeploy: async (id, refresh) => {
        const result = await attempt(shipEnvironment({ path: env, body: { only: [id], refresh } }));
        if (result.ok) link.open(result.data.id);
      },
      move: (id, position) => {
        const settle = overlay.move(id, position);
        void (async () => {
          await editNodes((nodes) => nodes.map((n) => (n.id === id ? { ...n, position } : n)));
          const result = await attempt(moveNode({ path: { id }, body: position }));
          await settled(result.ok, settle);
        })();
      },
      rename: async (id, name) => {
        await attempt(updateNode({ path: { id }, body: { name } }));
      },
      expose: async (id, options) =>
        (await attempt(exposeNode({ path: { id }, body: options ?? {} }))).ok,
      unexpose: async (id, endpoint) => {
        await attempt(
          unexposeNode({
            path: { id },
            body: endpoint
              ? {
                  protocol: endpoint.protocol,
                  domain: endpoint.domain,
                  publicPort: endpoint.publicPort,
                }
              : {},
          }),
        );
      },
      duplicate: async (id) => {
        const result = await attempt(duplicateNode({ path: { id } }));
        if (result.ok) overlay.arrive(result.data.id);
      },
      removeNodes: (ids) => {
        if (ids.length === 0) return;
        const settles = ids.map((id) => overlay.remove(id));
        const gone = new Set(ids);
        void (async () => {
          await editNodes((nodes) => nodes.filter((n) => !gone.has(n.id)));
          await Promise.all(
            ids.map(async (id, i) => {
              const result = await attempt(deleteNode({ path: { id } }));
              await settled(result.ok, settles[i]!);
            }),
          );
        })();
      },
      ship: async (only) => {
        const result = await attempt(
          shipEnvironment({ path: env, body: { only, refresh: only !== undefined } }),
        );
        if (result.ok) link.open(result.data.id);
      },
      overlay,
    };
  }, [
    environmentId,
    link,
    queryClient,
    overlay,
    createNode,
    updateNode,
    duplicateNode,
    shipEnvironment,
    startNode,
    stopNode,
    moveNode,
    deleteNode,
    exposeNode,
    unexposeNode,
  ]);

  return <Context value={value}>{children}</Context>;
}

export function useCanvasActions(): CanvasActions {
  const actions = useContext(Context);
  if (!actions) throw new Error("useCanvasActions must be used inside <CanvasActionsProvider>");
  return actions;
}
