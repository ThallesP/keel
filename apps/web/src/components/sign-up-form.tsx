import { Button } from "@my-better-t-app/ui/components/button";
import { Input } from "@my-better-t-app/ui/components/input";
import { Label } from "@my-better-t-app/ui/components/label";
import { useForm } from "@tanstack/react-form";
import { toast } from "sonner";
import z from "zod";

import { authClient } from "@/lib/auth-client";

/** An invite link (`/invite/$invitationId`): the account it creates joins that organization. */
export type Invitation = { id: string; email: string; organization: string };

export default function SignUpForm({
  onSwitchToSignIn,
  invitation,
  onSuccess,
}: {
  onSwitchToSignIn?: () => void;
  invitation?: Invitation;
  onSuccess?: () => void;
}) {
  const form = useForm({
    defaultValues: {
      email: invitation?.email ?? "",
      password: "",
      name: "",
    },
    onSubmit: async ({ value }) => {
      // `invitationId` is not a user field; the server's sign-up hook reads it from the body
      // (convex/auth.ts) to admit the account and add it to the organization.
      const body = {
        email: value.email,
        password: value.password,
        name: value.name,
        ...(invitation && { invitationId: invitation.id }),
      };
      await authClient.signUp.email(body, {
        onSuccess: () => {
          toast.success("Sign up successful");
          onSuccess?.();
        },
        onError: (error) => {
          toast.error(error.error.message || error.error.statusText);
        },
      });
    },
    validators: {
      onSubmit: z.object({
        name: z.string().min(2, "Name must be at least 2 characters"),
        email: z.email("Invalid email address"),
        password: z.string().min(8, "Password must be at least 8 characters"),
      }),
    },
  });

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
          form.handleSubmit();
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
              {isSubmitting ? "Creating account…" : invitation ? "Join" : "Create account"}
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
