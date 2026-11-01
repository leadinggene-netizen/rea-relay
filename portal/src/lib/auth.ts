/**
 * Auth.js (NextAuth v5) configuration.
 *
 * Two providers:
 *  1. Microsoft Entra ID (Azure AD) — the production sign-in, driven entirely
 *     by env vars (AZURE_AD_CLIENT_ID / _SECRET / _TENANT_ID). The button is
 *     only shown + the provider only registered when those are present, so the
 *     app runs locally without a real Entra tenant.
 *  2. Dev Credentials — email + any password maps to a stub user. Enabled only
 *     when NODE_ENV !== 'production' OR AUTH_DEV_LOGIN=1. NEVER enable in prod.
 *
 * Session strategy is JWT (no database adapter), so authentication needs no
 * live Postgres — the build and unit tests pass without one. Tenant/role is
 * carried on the token. The Drizzle models exist for the persistence slice.
 */
import NextAuth, { type NextAuthConfig } from "next-auth";
import Credentials from "next-auth/providers/credentials";
import MicrosoftEntraID from "next-auth/providers/microsoft-entra-id";

export const devLoginEnabled =
  process.env.NODE_ENV !== "production" || process.env.AUTH_DEV_LOGIN === "1";

export const entraConfigured = Boolean(
  process.env.AZURE_AD_CLIENT_ID &&
    process.env.AZURE_AD_CLIENT_SECRET &&
    process.env.AZURE_AD_TENANT_ID,
);

function buildProviders(): NextAuthConfig["providers"] {
  const providers: NextAuthConfig["providers"] = [];

  if (entraConfigured) {
    providers.push(
      MicrosoftEntraID({
        clientId: process.env.AZURE_AD_CLIENT_ID,
        clientSecret: process.env.AZURE_AD_CLIENT_SECRET,
        issuer: `https://login.microsoftonline.com/${process.env.AZURE_AD_TENANT_ID}/v2.0`,
      }),
    );
  }

  if (devLoginEnabled) {
    providers.push(
      Credentials({
        id: "dev-credentials",
        name: "Developer login",
        credentials: {
          email: { label: "Email", type: "email" },
          password: { label: "Password", type: "password" },
        },
        authorize: async (credentials) => {
          const email =
            typeof credentials?.email === "string" ? credentials.email : "";
          if (!email) return null;
          // Dev stub: accept any password, mint a deterministic user. The real
          // tenant lookup happens in the persistence slice.
          return {
            id: `dev:${email}`,
            email,
            name: email.split("@")[0],
          };
        },
      }),
    );
  }

  return providers;
}

export const authConfig: NextAuthConfig = {
  providers: buildProviders(),
  session: { strategy: "jwt" },
  pages: { signIn: "/signin" },
  callbacks: {
    authorized({ auth }) {
      return Boolean(auth?.user);
    },
    jwt({ token, user }) {
      if (user?.id) token.uid = user.id;
      return token;
    },
    session({ session, token }) {
      if (session.user && typeof token.uid === "string") {
        session.user.id = token.uid;
      }
      return session;
    },
  },
};

export const { handlers, auth, signIn, signOut } = NextAuth(authConfig);
