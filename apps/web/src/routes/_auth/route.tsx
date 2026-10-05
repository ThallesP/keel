import { Outlet, createFileRoute } from "@tanstack/react-router";
import { Authenticated, AuthLoading, Unauthenticated } from "convex/react";

import { AuthShell } from "@/components/auth/shell";
import { AuthForms } from "@/components/auth-forms";
import Loader from "@/components/loader";

export const Route = createFileRoute("/_auth")({
  component: AuthLayout,
});

function AuthLayout() {
  return (
    <>
      <Authenticated>
        <Outlet />
      </Authenticated>
      <Unauthenticated>
        <AuthShell>
          <AuthForms />
        </AuthShell>
      </Unauthenticated>
      <AuthLoading>
        <div className="h-svh bg-canvas">
          <Loader />
        </div>
      </AuthLoading>
    </>
  );
}
