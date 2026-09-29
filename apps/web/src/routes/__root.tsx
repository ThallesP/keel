import { Toaster } from "@my-better-t-app/ui/components/sonner";
import { HeadContent, Outlet, createRootRouteWithContext } from "@tanstack/react-router";
import { TanStackRouterDevtools } from "@tanstack/react-router-devtools";

import "../index.css";

export interface RouterAppContext {}

export const Route = createRootRouteWithContext<RouterAppContext>()({
  component: RootComponent,
  head: () => ({
    meta: [{ title: "keel" }, { name: "description", content: "Deploy canvas" }],
    links: [{ rel: "icon", href: "/favicon.ico" }],
  }),
});

function RootComponent() {
  return (
    <>
      <HeadContent />
      <Outlet />
      <Toaster richColors theme="light" />
      <TanStackRouterDevtools position="bottom-right" />
    </>
  );
}
