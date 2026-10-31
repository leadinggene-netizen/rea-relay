import Link from "next/link";
import { PricingTable } from "@/components/PricingTable";

export default function LandingPage() {
  return (
    <main className="mx-auto max-w-5xl px-6 py-16">
      <header className="flex items-center justify-between">
        <span className="font-semibold tracking-tight">rea-relay</span>
        <Link
          href="/signin"
          className="rounded-lg bg-white/10 px-4 py-2 text-sm font-medium hover:bg-white/20"
        >
          Sign in
        </Link>
      </header>

      <section className="mt-20 text-center">
        <h1 className="mx-auto max-w-3xl text-4xl font-bold tracking-tight sm:text-5xl">
          TradingView alerts into MT4/MT5, hosted and resolved server-side.
        </h1>
        <p className="mx-auto mt-5 max-w-2xl text-lg text-white/60">
          Point your TradingView strategy at rea-relay. We authenticate,
          deduplicate and risk-resolve every signal, then deliver fully-formed
          orders to a hosted MT5 cell with at-least-once delivery — no VPS to
          babysit. Priced to undercut the incumbent.
        </p>
        <div className="mt-8 flex justify-center gap-3">
          <Link
            href="/signin"
            className="rounded-lg bg-indigo-500 px-5 py-2.5 text-sm font-medium text-white hover:bg-indigo-400"
          >
            Start free
          </Link>
          <a
            href="#pricing"
            className="rounded-lg bg-white/10 px-5 py-2.5 text-sm font-medium hover:bg-white/20"
          >
            See pricing
          </a>
        </div>
      </section>

      <section className="mt-20 grid gap-4 sm:grid-cols-3">
        {[
          {
            t: "Server-side risk",
            d: "Volume, SL/TP and spread gates resolved on the relay — your alert just says what and where.",
          },
          {
            t: "Multi-account fan-out",
            d: "Mirror one signal across accounts and brokers with independent per-account results.",
          },
          {
            t: "Never miss a fill",
            d: "Offline buffer with resume-token replay means a dropped terminal catches up on reconnect.",
          },
        ].map((f) => (
          <div
            key={f.t}
            className="rounded-xl border border-white/10 bg-white/5 p-5"
          >
            <h3 className="font-semibold">{f.t}</h3>
            <p className="mt-2 text-sm text-white/60">{f.d}</p>
          </div>
        ))}
      </section>

      <section id="pricing" className="mt-24 scroll-mt-10">
        <div className="text-center">
          <h2 className="text-2xl font-bold tracking-tight">
            Pricing by concurrent connection
          </h2>
          <p className="mt-2 text-sm text-white/60">
            One connection = one hosted MT5 cell. Every tier undercuts the
            incumbent on the same connection count.
          </p>
        </div>
        <div className="mt-10">
          <PricingTable />
        </div>
      </section>

      <footer className="mt-24 border-t border-white/10 pt-8 text-center text-xs text-white/40">
        rea-relay · hosted TradingView → MT5 bridge
      </footer>
    </main>
  );
}
