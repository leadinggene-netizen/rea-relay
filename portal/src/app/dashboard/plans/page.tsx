import Link from "next/link";
import {
  PLANS,
  getPlan,
  formatPrice,
  pricePerConnectionCents,
} from "@/lib/plans";

export const metadata = { title: "Choose a plan — rea-relay" };

export default async function PlansPage({
  searchParams,
}: {
  searchParams: Promise<{ selected?: string }>;
}) {
  const { selected } = await searchParams;
  const selectedPlan = selected ? getPlan(selected) : undefined;

  return (
    <div>
      <h1 className="text-xl font-bold tracking-tight">Choose a plan</h1>
      <p className="mt-1 text-sm text-white/50">
        Each connection is one hosted MT5 cell. You can change tiers later.
      </p>

      <div className="mt-6 grid gap-4 sm:grid-cols-3">
        {PLANS.map((plan) => {
          const isSelected = selectedPlan?.tier === plan.tier;
          return (
            <div
              key={plan.tier}
              className={`flex flex-col rounded-2xl border p-5 ${
                isSelected
                  ? "border-indigo-400 bg-indigo-500/10"
                  : "border-white/10 bg-white/5"
              }`}
            >
              <h3 className="font-semibold">{plan.name}</h3>
              <div className="mt-2 flex items-end gap-1">
                <span className="text-2xl font-bold">
                  {formatPrice(plan.monthlyPriceCents)}
                </span>
                <span className="pb-1 text-xs text-white/50">/mo</span>
              </div>
              <p className="mt-1 text-xs text-white/50">
                {plan.maxConnections} connection
                {plan.maxConnections > 1 ? "s" : ""} ·{" "}
                {formatPrice(pricePerConnectionCents(plan))}/conn
              </p>
              <Link
                href={`/dashboard/plans?selected=${plan.tier}`}
                scroll={false}
                className={`mt-5 rounded-lg px-4 py-2 text-center text-sm font-medium transition ${
                  isSelected
                    ? "bg-indigo-500 text-white"
                    : "bg-white/10 hover:bg-white/20"
                }`}
              >
                {isSelected ? "Selected" : "Select"}
              </Link>
            </div>
          );
        })}
      </div>

      {selectedPlan && (
        <div className="mt-8 rounded-xl border border-white/10 bg-white/5 p-6">
          <h2 className="text-lg font-semibold">
            Continue with {selectedPlan.name}
          </h2>
          <p className="mt-1 text-sm text-white/60">
            {formatPrice(selectedPlan.monthlyPriceCents)}/mo ·{" "}
            {selectedPlan.maxConnections} concurrent connection
            {selectedPlan.maxConnections > 1 ? "s" : ""}.
          </p>
          {/* Posts to the checkout route, which creates a Stripe Checkout
              session and 303-redirects to the hosted payment page. */}
          <form method="POST" action="/api/billing/checkout" className="mt-4">
            <input type="hidden" name="tier" value={selectedPlan.tier} />
            <button
              type="submit"
              className="rounded-lg bg-indigo-500 px-5 py-2.5 text-sm font-medium text-white transition hover:bg-indigo-400"
            >
              Continue to checkout →
            </button>
          </form>
          <p className="mt-2 text-xs text-white/40">
            You&apos;ll be redirected to Stripe&apos;s secure checkout to enter
            payment details.
          </p>
        </div>
      )}
    </div>
  );
}
