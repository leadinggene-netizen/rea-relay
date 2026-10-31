import Link from "next/link";
import { auth } from "@/lib/auth";
import {
  getConnectionsProvider,
  type ConnectionView,
} from "@/lib/connections";

// Connections are read per-request from the configured provider (stub or
// relay), never at build time.
export const dynamic = "force-dynamic";

const STATUS_STYLES: Record<ConnectionView["status"], string> = {
  active: "bg-emerald-500/15 text-emerald-300",
  provisioning: "bg-sky-500/15 text-sky-300",
  pending: "bg-amber-500/15 text-amber-300",
  paused: "bg-white/10 text-white/60",
  error: "bg-rose-500/15 text-rose-300",
};

export default async function ConnectionsPage() {
  const session = await auth();
  const ownerId = session?.user?.id ?? session?.user?.email ?? "unknown";

  const provider = getConnectionsProvider();
  let connections: ConnectionView[] = [];
  let error: string | null = null;
  try {
    connections = await provider.listForOwner(ownerId);
  } catch (e) {
    error = e instanceof Error ? e.message : "Failed to load connections";
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold tracking-tight">Connections</h1>
          <p className="mt-1 text-sm text-white/50">
            Hosted MT5 cells provisioned for your subscription.
          </p>
        </div>
        <span className="rounded-full bg-white/5 px-3 py-1 text-xs text-white/40">
          data source: {provider.source}
        </span>
      </div>

      {error && (
        <div className="mt-6 rounded-lg border border-rose-500/30 bg-rose-500/10 p-4 text-sm text-rose-200">
          {error}
        </div>
      )}

      {!error && connections.length === 0 && (
        <div className="mt-6 rounded-lg border border-white/10 bg-white/5 p-8 text-center">
          <p className="text-sm text-white/60">
            No connections yet. Choose a plan to provision your first.
          </p>
          <Link
            href="/dashboard/plans"
            className="mt-4 inline-block rounded-lg bg-indigo-500 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-400"
          >
            Choose a plan
          </Link>
        </div>
      )}

      {!error && connections.length > 0 && (
        <div className="mt-6 overflow-hidden rounded-xl border border-white/10">
          <table className="w-full text-left text-sm">
            <thead className="bg-white/5 text-xs uppercase tracking-wide text-white/40">
              <tr>
                <th className="px-4 py-3 font-medium">Label</th>
                <th className="px-4 py-3 font-medium">Key</th>
                <th className="px-4 py-3 font-medium">Broker</th>
                <th className="px-4 py-3 font-medium">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-white/5">
              {connections.map((c) => (
                <tr key={c.id}>
                  <td className="px-4 py-3">{c.label}</td>
                  <td className="px-4 py-3 font-mono text-xs text-white/60">
                    {c.key}
                  </td>
                  <td className="px-4 py-3 text-white/60">
                    {c.brokerServer ?? "—"}
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className={`rounded-full px-2 py-0.5 text-xs font-medium ${STATUS_STYLES[c.status]}`}
                    >
                      {c.status}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
