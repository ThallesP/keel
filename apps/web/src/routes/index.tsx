import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Authenticated, AuthLoading, Unauthenticated, useMutation } from "convex/react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

import Loader from "@/components/loader";
import SignInForm from "@/components/sign-in-form";
import SignUpForm from "@/components/sign-up-form";

export const Route = createFileRoute("/")({ component: Index });

/** Creates the user's first project on first use, then jumps to its canvas. */
function Bootstrap() {
  const ensureDefault = useMutation(api.projects.ensureDefault);
  const navigate = useNavigate();
  useEffect(() => {
    ensureDefault({})
      .then((slug) => navigate({ to: "/p/$projectId", params: { projectId: slug }, replace: true }))
      .catch((err: Error) => toast.error(err.message));
  }, [ensureDefault, navigate]);
  return <Loader />;
}

function Index() {
  const [showSignIn, setShowSignIn] = useState(true);
  return (
    <div className="h-svh bg-canvas">
      <Authenticated>
        <Bootstrap />
      </Authenticated>
      <Unauthenticated>
        <div className="flex h-full items-center justify-center">
          {showSignIn ? (
            <SignInForm onSwitchToSignUp={() => setShowSignIn(false)} />
          ) : (
            <SignUpForm onSwitchToSignIn={() => setShowSignIn(true)} />
          )}
        </div>
      </Unauthenticated>
      <AuthLoading>
        <Loader />
      </AuthLoading>
    </div>
  );
}
