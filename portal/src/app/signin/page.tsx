import Link from "next/link";
import { signIn, devLoginEnabled, entraConfigured } from "@/lib/auth";

export const metadata = { title: "Sign in — rea-relay" };

export default function SignInPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-md flex-col justify-center px-6 py-16">
      <Link href="/" className="mb-8 text-sm text-white/50 hover:text-white">
        ← rea-relay
      </Link>
      <h1 className="text-2xl font-bold tracking-tight">Sign in</h1>
      <p className="mt-2 text-sm text-white/60">
        Access your connections and subscription.
      </p>

      {entraConfigured && (
        <form
          action={async () => {
            "use server";
            await signIn("microsoft-entra-id", { redirectTo: "/dashboard" });
          }}
          className="mt-8"
        >
          <button
            type="submit"
            className="w-full rounded-lg bg-white/10 px-4 py-2.5 text-sm font-medium hover:bg-white/20"
          >
            Continue with Microsoft Entra ID
          </button>
        </form>
      )}

      {entraConfigured && devLoginEnabled && (
        <div className="my-6 flex items-center gap-3 text-xs text-white/40">
          <span className="h-px flex-1 bg-white/10" />
          or
          <span className="h-px flex-1 bg-white/10" />
        </div>
      )}

      {devLoginEnabled && (
        <form
          action={async (formData: FormData) => {
            "use server";
            await signIn("dev-credentials", {
              email: formData.get("email"),
              password: formData.get("password"),
              redirectTo: "/dashboard",
            });
          }}
          className="mt-4 space-y-3"
        >
          <div className="rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
            Dev login: any password is accepted. Disabled in production.
          </div>
          <label className="block text-sm">
            <span className="text-white/60">Email</span>
            <input
              name="email"
              type="email"
              required
              placeholder="you@example.com"
              className="mt-1 w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm outline-none focus:border-indigo-400"
            />
          </label>
          <label className="block text-sm">
            <span className="text-white/60">Password</span>
            <input
              name="password"
              type="password"
              placeholder="anything"
              className="mt-1 w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm outline-none focus:border-indigo-400"
            />
          </label>
          <button
            type="submit"
            className="w-full rounded-lg bg-indigo-500 px-4 py-2.5 text-sm font-medium text-white hover:bg-indigo-400"
          >
            Sign in (dev)
          </button>
        </form>
      )}

      {!entraConfigured && !devLoginEnabled && (
        <p className="mt-8 rounded-lg border border-white/10 bg-white/5 p-4 text-sm text-white/60">
          No sign-in provider is configured. Set the AZURE_AD_* env vars for
          Microsoft Entra ID, or AUTH_DEV_LOGIN=1 to enable the dev login.
        </p>
      )}
    </main>
  );
}
