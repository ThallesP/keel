import { useState } from "react";

import { useGetSignUpOpen } from "@/gen/api";
import Loader from "@/components/loader";
import SignInForm from "@/components/sign-in-form";
import SignUpForm from "@/components/sign-up-form";

/**
 * Sign-in, plus sign-up only while the install has no account yet: the first account founds the
 * organization, everyone after it signs up from an invite link (`/invite/$invitationId`).
 */
export function AuthForms() {
  const { data: open } = useGetSignUpOpen({ query: { select: (s) => s.open } });
  const [showSignIn, setShowSignIn] = useState<boolean | null>(null);
  if (open === undefined) return <Loader />;
  // A fresh install lands on sign-up; once an account exists only sign-in is offered.
  const signIn = showSignIn ?? !open;
  return signIn ? (
    <SignInForm onSwitchToSignUp={open ? () => setShowSignIn(false) : undefined} />
  ) : (
    <SignUpForm onSwitchToSignIn={() => setShowSignIn(true)} />
  );
}
