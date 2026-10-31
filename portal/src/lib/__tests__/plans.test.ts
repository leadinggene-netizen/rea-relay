import { describe, it, expect } from "vitest";
import {
  PLANS,
  getPlan,
  formatPrice,
  pricePerConnectionCents,
  annualPriceCents,
  savingsVsIncumbentCents,
  savingsVsIncumbentPct,
} from "@/lib/plans";

describe("plan catalog", () => {
  it("defines the three mirrored tiers in ascending size", () => {
    expect(PLANS.map((p) => p.tier)).toEqual(["STARTER", "ADVANCED", "PRO"]);
    expect(PLANS.map((p) => p.maxConnections)).toEqual([1, 3, 5]);
  });

  it("undercuts the incumbent on every tier", () => {
    for (const p of PLANS) {
      expect(p.monthlyPriceCents).toBeLessThan(p.incumbentPriceCents);
    }
  });

  it("keeps each tier under the incumbent reference caps ($39/$79/$159)", () => {
    const caps = { STARTER: 3900, ADVANCED: 7900, PRO: 15900 } as const;
    for (const p of PLANS) {
      expect(p.monthlyPriceCents).toBeLessThan(caps[p.tier]);
    }
  });
});

describe("getPlan", () => {
  it("resolves a known tier", () => {
    expect(getPlan("ADVANCED")?.name).toBe("Advanced");
  });
  it("returns undefined for an unknown tier", () => {
    expect(getPlan("ENTERPRISE")).toBeUndefined();
  });
});

describe("formatPrice", () => {
  it("drops cents for whole-dollar amounts", () => {
    expect(formatPrice(2900)).toBe("$29");
    expect(formatPrice(14900)).toBe("$149");
  });
  it("shows cents when present", () => {
    expect(formatPrice(2999)).toBe("$29.99");
  });
  it("adds thousands separators", () => {
    expect(formatPrice(123456)).toBe("$1,234.56");
  });
});

describe("pricePerConnectionCents", () => {
  it("divides monthly price across connections (rounded)", () => {
    const advanced = getPlan("ADVANCED")!;
    expect(pricePerConnectionCents(advanced)).toBe(Math.round(6900 / 3));
  });
  it("handles a zero-connection plan without dividing by zero", () => {
    const fake = { ...getPlan("STARTER")!, maxConnections: 0 };
    expect(pricePerConnectionCents(fake)).toBe(fake.monthlyPriceCents);
  });
});

describe("annualPriceCents", () => {
  it("charges 10 of 12 months by default", () => {
    const starter = getPlan("STARTER")!;
    expect(annualPriceCents(starter)).toBe(2900 * 10);
  });
  it("honors a custom months-free value", () => {
    const starter = getPlan("STARTER")!;
    expect(annualPriceCents(starter, 0)).toBe(2900 * 12);
  });
});

describe("savings vs incumbent", () => {
  it("computes positive savings for a cheaper plan", () => {
    const starter = getPlan("STARTER")!;
    expect(savingsVsIncumbentCents(starter)).toBe(3900 - 2900);
    expect(savingsVsIncumbentPct(starter)).toBe(26);
  });
  it("never reports negative savings", () => {
    const fake = { ...getPlan("STARTER")!, monthlyPriceCents: 9999 };
    expect(savingsVsIncumbentCents(fake)).toBe(0);
    expect(savingsVsIncumbentPct(fake)).toBe(0);
  });
});
