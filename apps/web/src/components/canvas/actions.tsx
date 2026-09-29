import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useMutation } from "convex/react";
import { createContext, useContext, useMemo, useRef, type ReactNode, type RefObject } from "react";

import { useEnvironment } from "./environment";
import { attempt } from "./errors";
import { asNodeId } from "./mapping";
import type { InfraNodeType } from "./types";
import { useDeploymentLink } from "./use-deployment-link";

type Position = { x: number; y: number };

/**
 * Service: an image to run. Database / cache: an engine from the catalog. Omit for the type
 * default. `deploy` ships the node immediately and opens its Deployments tab.
 */
export type CreateOptions = {
  image?: string;
  engine?: "postgres" | "mysql" | "mongo" | "redis";
  deploy?: boolean;
};

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
  duplicate: (id: string) => Promise<void>;
  removeNodes: (ids: string[]) => void;
  /** Ship every dirty node, or `only` these (re-pulling their images). */
  ship: (only?: string[]) => Promise<void>;
  /** Ids created by this client; the graph sync selects them once the query delivers them. */
  selectOnArrival: RefObject<Set<string>>;
};

const Context = createContext<CanvasActions | null>(null);

/** Every mutation the canvas fires, scoped to the current environment. Failures become toasts. */
export function CanvasActionsProvider({ children }: { children: ReactNode }) {
  const { environmentId } = useEnvironment();
  const link = useDeploymentLink();
  const selectOnArrival = useRef(new Set<string>());

  const createNode = useMutation(api.nodes.create);
  const renameNode = useMutation(api.nodes.rename);
  const duplicateNode = useMutation(api.nodes.duplicate);
  const startDeployment = useMutation(api.deployments.start);
  const startNode = useMutation(api.nodes.start);
  const stopNode = useMutation(api.nodes.stop);
  const moveRaw = useMutation(api.nodes.move);
  const removeNodeRaw = useMutation(api.nodes.remove);

  // Drag end and delete update the local graph instantly; optimistic updates keep the
  // subscription in step so a concurrent server push cannot snap things back.
  const moveNode = useMemo(
    () =>
      moveRaw.withOptimisticUpdate((store, { id, position }) => {
        const nodes = store.getQuery(api.nodes.list, { environmentId });
        if (!nodes) return;
        const next = nodes.map((n) => (n.id === id ? { ...n, position } : n));
        store.setQuery(api.nodes.list, { environmentId }, next);
      }),
    [moveRaw, environmentId],
  );
  const removeNode = useMemo(
    () =>
      removeNodeRaw.withOptimisticUpdate((store, { id }) => {
        const nodes = store.getQuery(api.nodes.list, { environmentId });
        if (!nodes) return;
        const next = nodes.filter((n) => n.id !== id);
        store.setQuery(api.nodes.list, { environmentId }, next);
      }),
    [removeNodeRaw, environmentId],
  );

  const value = useMemo<CanvasActions>(
    () => ({
      create: async (type, position, options) => {
        const result = await attempt(createNode({ environmentId, type, position, ...options }));
        if (!result) return;
        selectOnArrival.current.add(result.id);
        if (result.deploymentId) link.open(result.deploymentId);
      },
      start: async (id) => {
        const did = await attempt(startNode({ id: asNodeId(id) }));
        if (did) link.open(did);
      },
      stop: async (id) => {
        const did = await attempt(stopNode({ id: asNodeId(id) }));
        if (did) link.open(did);
      },
      redeploy: async (id, refresh) => {
        const did = await attempt(
          startDeployment({ environmentId, only: [asNodeId(id)], refresh }),
        );
        if (did) link.open(did);
      },
      move: (id, position) => void attempt(moveNode({ id: asNodeId(id), position })),
      rename: async (id, name) => {
        await attempt(renameNode({ id: asNodeId(id), name }));
      },
      duplicate: async (id) => {
        const copy = await attempt(duplicateNode({ id: asNodeId(id) }));
        if (copy) selectOnArrival.current.add(copy);
      },
      removeNodes: (ids) => {
        for (const id of ids) void attempt(removeNode({ id: asNodeId(id) }));
      },
      ship: async (only) => {
        const id = await attempt(
          startDeployment({
            environmentId,
            only: only?.map(asNodeId),
            refresh: only !== undefined,
          }),
        );
        if (id) link.open(id);
      },
      selectOnArrival,
    }),
    [
      environmentId,
      link,
      createNode,
      moveNode,
      renameNode,
      duplicateNode,
      removeNode,
      startDeployment,
      startNode,
      stopNode,
    ],
  );

  return <Context value={value}>{children}</Context>;
}

export function useCanvasActions(): CanvasActions {
  const actions = useContext(Context);
  if (!actions) throw new Error("useCanvasActions must be used inside <CanvasActionsProvider>");
  return actions;
}
