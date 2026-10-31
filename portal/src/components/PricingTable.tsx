import Link from "next/link";
import {
  PLANS,
  formatPrice,
  savingsVsIncumbentPct,
  type PlanDef,
} from "@/lib/plans";

function PlanCard({
  plan,
  ctaHref,
  ctaLabel,
}: {
  plan: PlanDef;
  ctaHref: string;
  ctaLabel: string;
}) {
  const savings = savingsVsIncumbentPct(plan);
  return (
    <div
      className={`flex flex-col rounded-2xl border p-6 ${
        plan.highlighted
          ? "border-indigo-400/60 bg-indigo-500/10"
          : "border-white/10 bg-white/5"
      }`}
    >
      <div className="flex items-baseline justify-between">
        <h3 className="text-lg font-semibold">{plan.name}</h3>
        {savings > 0 && (
          <span className="rounded-full bg-emerald-500/15 px-2 py-0.5 text-xs font-medium text-emerald-300">
            {savings}% cheaper
          </span>
        )}
      </div>
      <p className="mt-1 text-sm text-white/60">{plan.tagline}</p>
      <div className="mt-4 flex items-end gap-1">
        <span className="text-3xl font-bold">
          {formatPrice(plan.monthlyPriceCents)}
        </span>
        <span className="pb-1 text-sm text-white/50">/mo</span>
      </div>
      <p className="mt-1 text-xs text-white/40 line-through">
        incumbent {formatPrice(plan.incumbentPriceCents)}/mo
      </p>
      <ul className="mt-5 flex-1 space-y-2 text-sm text-white/70">
        {plan.features.map((f) => (
          <li key={f} className="flex gap-2">
            <span aria-hidden className="text-emerald-400">
              ✓
            </span>
            {f}
          </li>
        ))}
      </ul>
      <Link
        href={ctaHref}
        className={`mt-6 rounded-lg px-4 py-2 text-center text-sm font-medium transition ${
          plan.highlighted
            ? "bg-indigo-500 text-white hover:bg-indigo-400"
            : "bg-white/10 text-white hover:bg-white/20"
        }`}
      >
        {ctaLabel}
      </Link>
    </div>
  );
}

export function PricingTable({
  ctaHref = "/signin",
  ctaLabel = "Get started",
}: {
  ctaHref?: string;
  ctaLabel?: string;
}) {
  return (
    <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
      {PLANS.map((plan) => (
        <PlanCard
          key={plan.tier}
          plan={plan}
          ctaHref={ctaHref}
          ctaLabel={ctaLabel}
        />
      ))}
    </div>
  );
}
