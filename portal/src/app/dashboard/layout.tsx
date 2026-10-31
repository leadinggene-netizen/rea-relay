import Link from "next/link";
import { redirect } from "next/navigation";
import { auth, signOut } from "@/lib/auth";

export default async function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const session = await auth();
  if (!session?.user) {
    redirect("/signin");
  }

  return (
    <div className="mx-auto max-w-5xl px-6 py-10">
      <header className="flex items-center justify-between border-b border-white/10 pb-5">
        <div className="flex items-center gap-6">
          <Link href="/dashboard" className="font-semibold tracking-tight">
            rea-relay
          </Link>
          <nav className="flex gap-4 text-sm text-white/60">
            <Link href="/dashboard" className="hover:text-white">
              Connections
            </Link>
            <Link href="/dashboard/plans" className="hover:text-white">
              Plans
            </Link>
          </nav>
        </div>
        <div className="flex items-center gap-4 text-sm">
          <span className="text-white/50">{session.user.email}</span>
          <form
            action={async () => {
              "use server";
              await signOut({ redirectTo: "/" });
            }}
          >
            <button
              type="submit"
              className="rounded-lg bg-white/10 px-3 py-1.5 font-medium hover:bg-white/20"
            >
              Sign out
            </button>
          </form>
        </div>
      </header>
      <main className="pt-8">{children}</main>
    </div>
  );
}
