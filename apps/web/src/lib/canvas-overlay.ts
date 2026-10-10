// The canvas's optimistic overlay (docs/go/spec/web-data.md §7, §8.5). No React here;
// components/canvas/actions.tsx owns one per environment and use-synced-graph.ts reads it.
//
// React Flow already shows a drag or a delete locally. Until the write settles, a refetch of
// `GET /api/environments/{id}/nodes` that lands first (another node's status change, a deploy log
// line, the socket's invalidation of someone else's write) would snap the node back or bring it
// back. The overlay is re-applied on top of every answer until the write settles, the way Convex
// re-applied a pending optimistic update on top of every server value.
//
// It also carries the ids this tab created (create, duplicate) that the sync selects once a node
// list contains them. Queuing one bumps the version, so the sync runs again with the list it
// already has: the select works whether the list delivered the node before or after the write
// resolved (read-your-writes refetches before it resolves).
import type { NodeView, Position } from "@/gen/api";

export class CanvasOverlay {
  #seq = 0;
  #moves = new Map<string, Position & { seq: number }>();
  #removals = new Map<string, number>();
  #arrivals = new Set<string>();
  #version = 0;
  #listeners = new Set<() => void>();

  /**
   * Holds `id` at `position` until the returned `settle` runs. A later move of the same node
   * replaces this one, and then this `settle` does nothing (two quick drags: the first answer
   * must not clear the second).
   */
  move(id: string, position: Position): () => void {
    const seq = ++this.#seq;
    this.#moves.set(id, { x: position.x, y: position.y, seq });
    this.#changed();
    return () => {
      if (this.#moves.get(id)?.seq !== seq) return;
      this.#moves.delete(id);
      this.#changed();
    };
  }

  /** Hides `id` until the returned `settle` runs (same latest-wins rule as `move`). */
  remove(id: string): () => void {
    const seq = ++this.#seq;
    this.#removals.set(id, seq);
    this.#changed();
    return () => {
      if (this.#removals.get(id) !== seq) return;
      this.#removals.delete(id);
      this.#changed();
    };
  }

  /** Selects `id` once a node list contains it. */
  arrive(id: string): void {
    this.#arrivals.add(id);
    this.#changed();
  }

  /** The queued arrivals `nodes` contains, removed from the queue (each is selected once). */
  takeArrivals(nodes: readonly { id: string }[]): Set<string> {
    const arrived = new Set<string>();
    if (this.#arrivals.size === 0) return arrived;
    for (const n of nodes) {
      if (this.#arrivals.delete(n.id)) arrived.add(n.id);
    }
    return arrived;
  }

  /** `nodes` with pending moves applied and pending removals left out (the same array if none). */
  apply(nodes: NodeView[]): NodeView[] {
    if (this.#moves.size === 0 && this.#removals.size === 0) return nodes;
    return nodes
      .filter((n) => !this.#removals.has(n.id))
      .map((n) => {
        const move = this.#moves.get(n.id);
        return move ? { ...n, position: { x: move.x, y: move.y } } : n;
      });
  }

  /** For useSyncExternalStore: `getVersion` changes whenever the overlay does. */
  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  };

  getVersion = (): number => this.#version;

  #changed() {
    this.#version++;
    for (const listener of this.#listeners) listener();
  }
}
