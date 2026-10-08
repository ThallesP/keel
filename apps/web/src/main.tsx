import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";

import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import ReactDOM from "react-dom/client";

import { setupApiClient } from "@/lib/api";
import { createQueryClient } from "@/lib/query";
import { RealtimeProvider } from "@/lib/realtime";

import Loader from "./components/loader";
import { routeTree } from "./routeTree.gen";

// Dashboard, API and WebSocket share this origin (keel serve; Vite proxies them in dev), so there
// is nothing to configure at runtime: the client calls relative `/api/...` with the session cookie.
const queryClient = createQueryClient();
setupApiClient(queryClient);

const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  scrollRestoration: true,
  defaultPendingComponent: () => <Loader />,
  context: { queryClient },
  Wrap: function WrapComponent({ children }: { children: React.ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        <RealtimeProvider>{children}</RealtimeProvider>
      </QueryClientProvider>
    );
  },
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const rootElement = document.getElementById("app");

if (!rootElement) {
  throw new Error("Root element not found");
}

if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement);
  root.render(<RouterProvider router={router} />);
}
