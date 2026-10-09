import { Button } from "@my-better-t-app/ui/components/button";
import { Input } from "@my-better-t-app/ui/components/input";
import { Label } from "@my-better-t-app/ui/components/label";
import { useForm } from "@tanstack/react-form";
import { toast } from "sonner";
import z from "zod";

import type { PublicInvitation } from "@/api/gen";
import { errorMessage } from "@/lib/api";
import { useAuth } from "@/lib/session";

/** An invite link (`/invite/$invitationId`): the account it creates joins that organization. */
export type Invitation = PublicInvitation & { id: string };

export default function SignUpForm({
  onSwitchToSignIn,
  invitation,
  onSuccess,
}: {
  onSwitchToSignIn?: () => void;
  invitation?: Invitation;
  onSuccess?: () => void;
}) {
  const auth = useAuth();
  const form = useForm({
    defaultValues: {
      email: invitation?.email ?? "",
      password: "",
      name: "",
    },
    onSubmit: async ({ value }) => {
      // `invitationId` admits the account once the first one exists and makes it a member of
      // that invitation's organization (`POST /api/auth/sign-up`).
      try {
        await auth.signUp({
          email: value.email,
          password: value.password,
          name: value.name,
          ...(invitation && { invitationId: invitation.id }),
        });
      } catch (err) {
        toast.error(errorMessage(err));
        return;
      }
      toast.success("Sign up successful");
      onSuccess?.();
    },
    validators: {
      onSubmit: z.object({
        name: z.string().min(2, "Name must be at least 2 characters"),
        email: z.email("Invalid email address"),
        password: z.string().min(8, "Password must be at least 8 characters"),
      }),
    },
  });
  const submitLabel = invitation ? "Join" : "Create account";

  return (
    <div>
      <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">
        {invitation ? `Join ${invitation.organization}` : "Lay the keel"}
      </h1>
      <p className="mb-6 text-xs text-muted-foreground">
        {invitation
          ? "You were invited aboard. Pick a name and a password to create your account."
          : "This install has no account yet. The first one owns it; everyone after joins by invitation."}
      </p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          e.stopPropagation();
          void form.handleSubmit();
        }}
        className="space-y-4"
      >
        <div>
          <form.Field name="name">
            {(field) => (
              <div className="space-y-2">
                <Label htmlFor={field.name}>Name</Label>
                <Input
                  id={field.name}
                  name={field.name}
                  autoFocus
                  value={field.state.value}
                  onBlur={field.handleBlur}
                  onChange={(e) => field.handleChange(e.target.value)}
                />
                {field.state.meta.errors.map((error, index) => (
                  <p key={`${field.name}-error-${index}`} className="text-xs text-danger">
                    {error?.message}
                  </p>
                ))}
              </div>
            )}
          </form.Field>
        </div>

        <div>
          <form.Field name="email">
            {(field) => (
              <div className="space-y-2">
                <Label htmlFor={field.name}>Email</Label>
                <Input
                  id={field.name}
                  name={field.name}
                  type="email"
                  autoComplete="email"
                  value={field.state.value}
                  onBlur={field.handleBlur}
                  onChange={(e) => field.handleChange(e.target.value)}
                  disabled={invitation !== undefined}
                />
                {field.state.meta.errors.map((error, index) => (
                  <p key={`${field.name}-error-${index}`} className="text-xs text-danger">
                    {error?.message}
                  </p>
                ))}
              </div>
            )}
          </form.Field>
        </div>

        <div>
          <form.Field name="password">
            {(field) => (
              <div className="space-y-2">
                <Label htmlFor={field.name}>Password</Label>
                <Input
                  id={field.name}
                  name={field.name}
                  type="password"
                  autoComplete="new-password"
                  value={field.state.value}
                  onBlur={field.handleBlur}
                  onChange={(e) => field.handleChange(e.target.value)}
                />
                {field.state.meta.errors.map((error, index) => (
                  <p key={`${field.name}-error-${index}`} className="text-xs text-danger">
                    {error?.message}
                  </p>
                ))}
              </div>
            )}
          </form.Field>
        </div>

        <form.Subscribe
          selector={(state) => ({ canSubmit: state.canSubmit, isSubmitting: state.isSubmitting })}
        >
          {({ canSubmit, isSubmitting }) => (
            <Button type="submit" className="w-full" disabled={!canSubmit || isSubmitting}>
              {isSubmitting ? "Creating account…" : submitLabel}
            </Button>
          )}
        </form.Subscribe>
      </form>

      {onSwitchToSignIn && (
        <div className="mt-4 text-center">
          <Button variant="link" onClick={onSwitchToSignIn} className="text-primary">
            Already have an account? Sign in
          </Button>
        </div>
      )}
    </div>
  );
}
