import {
  createContext,
  useContext,
  useMemo,
  useReducer,
  useState,
  type Dispatch,
  type ReactNode,
} from "react";

import type { PanelTab } from "./types";

export type CanvasUiState = {
  /** Node shown in the bottom panel. Survives deselection so the collapsed strip keeps the name. */
  panelNodeId: string | null;
  panelTab: PanelTab;
  panelCollapsed: boolean;
};

export type CanvasUiAction =
  | { type: "select"; nodeId: string | null }
  | { type: "openTab"; nodeId: string; tab: PanelTab }
  | { type: "setTab"; tab: PanelTab }
  | { type: "collapsePanel"; collapsed: boolean };

const initialState: CanvasUiState = {
  panelNodeId: null,
  panelTab: "deployments",
  panelCollapsed: false,
};

function reducer(state: CanvasUiState, action: CanvasUiAction): CanvasUiState {
  switch (action.type) {
    case "select":
      // Deselecting keeps the last node but collapses to the 44px strip.
      if (action.nodeId === null) return { ...state, panelCollapsed: true };
      return {
        ...state,
        panelNodeId: action.nodeId,
        panelCollapsed: false,
        panelTab: action.nodeId === state.panelNodeId ? state.panelTab : "deployments",
      };
    case "openTab":
      return {
        ...state,
        panelNodeId: action.nodeId,
        panelTab: action.tab,
        panelCollapsed: false,
      };
    case "setTab":
      return { ...state, panelTab: action.tab, panelCollapsed: false };
    case "collapsePanel":
      return { ...state, panelCollapsed: action.collapsed };
  }
}

const StateContext = createContext<CanvasUiState | null>(null);
const DispatchContext = createContext<Dispatch<CanvasUiAction> | null>(null);

type Renaming = { renamingId: string | null; setRenamingId: (id: string | null) => void };
/** Separate from the reducer so node cards can subscribe without re-rendering on panel changes. */
const RenamingContext = createContext<Renaming | null>(null);

export function CanvasUiProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(reducer, initialState);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const renaming = useMemo(() => ({ renamingId, setRenamingId }), [renamingId]);
  return (
    <StateContext value={state}>
      <DispatchContext value={dispatch}>
        <RenamingContext value={renaming}>{children}</RenamingContext>
      </DispatchContext>
    </StateContext>
  );
}

/** Which node card is showing its inline name editor (⋯ → Rename, double-click, F2). */
export function useRenaming(): Renaming {
  const renaming = useContext(RenamingContext);
  if (!renaming) throw new Error("useRenaming must be used inside <CanvasUiProvider>");
  return renaming;
}

export function useCanvasUi(): CanvasUiState {
  const state = useContext(StateContext);
  if (!state) throw new Error("useCanvasUi must be used inside <CanvasUiProvider>");
  return state;
}

export function useCanvasDispatch(): Dispatch<CanvasUiAction> {
  const dispatch = useContext(DispatchContext);
  if (!dispatch) throw new Error("useCanvasDispatch must be used inside <CanvasUiProvider>");
  return dispatch;
}
