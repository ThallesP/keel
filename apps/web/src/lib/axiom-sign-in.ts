// Sign in with Axiom, browser half. The control plane makes the PKCE verifier and state
// (logSinks.beginAxiomSignIn; WebCrypto is missing on plain-http origins like a tailnet IP) and
// hands back the authorize URL. All the browser keeps is which project to return to.

const RETURN_KEY = "keel.axiom.return";

export const axiomRedirectUri = () => `${window.location.origin}/axiom/callback`;

export function goToAxiom(url: string, slug: string) {
  sessionStorage.setItem(RETURN_KEY, slug);
  window.location.assign(url);
}

/** Project slug the sign-in started from, removed on read. */
export function takeAxiomReturn(): string | null {
  const slug = sessionStorage.getItem(RETURN_KEY);
  sessionStorage.removeItem(RETURN_KEY);
  return slug;
}
