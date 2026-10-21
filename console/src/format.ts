import type { Outcome, PermissionStatus } from "./types";

/** 4800 -> "$48.00". Amounts always show cents so they read the same everywhere. */
export function money(cents: number): string {
  const sign = cents < 0 ? "-" : "";
  const abs = Math.abs(Math.trunc(cents));
  const dollars = Math.floor(abs / 100).toLocaleString("en-US");
  return `${sign}$${dollars}.${String(abs % 100).padStart(2, "0")}`;
}

/** "48", "48.5", "48.50" -> cents. Returns null for anything else. */
export function parseDollars(input: string): number | null {
  const s = input.trim().replace(/^\$/, "").replace(/,/g, "");
  if (!/^\d+(\.\d{0,2})?$/.test(s)) return null;
  const [whole, frac = ""] = s.split(".");
  return Number(whole) * 100 + Number(frac.padEnd(2, "0"));
}

export function dollarsInput(cents: number): string {
  return cents % 100 === 0 ? String(cents / 100) : (cents / 100).toFixed(2);
}

export const outcomeLabel: Record<Outcome, { text: string; icon: string; tone: string }> = {
  ALLOW: { text: "Allowed", icon: "✓", tone: "ok" },
  CONFIRM: { text: "Asked you", icon: "?", tone: "ask" },
  BLOCK: { text: "Blocked", icon: "✕", tone: "stop" },
};

export const statusLabel: Record<PermissionStatus, { text: string; icon: string; tone: string }> = {
  draft: { text: "Not signed", icon: "○", tone: "muted" },
  active: { text: "Active", icon: "●", tone: "ok" },
  paused: { text: "Paused", icon: "❚❚", tone: "ask" },
  revoked: { text: "Revoked", icon: "✕", tone: "stop" },
};

/** Plain words for reason codes, used in the inline "why" help. */
export const reasonHelp: Record<string, string> = {
  WITHIN_PERMISSION: "Every check passed.",
  PERMISSION_INACTIVE: "The permission is paused, revoked, expired, not signed, or its signature doesn't match.",
  CURRENCY_MISMATCH: "This permission only covers US dollars.",
  CATEGORY_NOT_ALLOWED: "The item's category isn't in the permission.",
  NEW_MERCHANT: "The store isn't on your list, so you're asked first.",
  MERCHANT_NOT_ALLOWED: "The store isn't on your list and asking for new stores is off.",
  RETURN_WINDOW_SHORT: "The store's return window is shorter than your minimum.",
  RETURN_TERMS_UNVERIFIED: "Warrant has no verified return policy for this store.",
  OVER_PURCHASE_CAP: "The purchase is above your per-purchase limit.",
  OVER_MONTHLY_BUDGET: "The purchase would take this month past your budget.",
  BUDGET_CONFLICT: "Another purchase was using the budget at the same moment. The agent can retry.",
  ABOVE_CONFIRM_THRESHOLD: "The purchase is above your approval line.",
  UNTRUSTED_INSTRUCTION: "The request came from a web page, a store's text or a tool, or something tried to change your limits.",
  PRICE_DRIFT: "The final price at checkout differed from what was approved.",
};

export const sourceLabel: Record<string, string> = {
  user: "you asked",
  agent: "agent's own choice",
  web: "a web page",
  merchant: "the store's text",
  tool_output: "a tool's output",
};

export function when(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("en-US", { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

export function day(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
}

/** base64 -> first 16 hex chars, for showing a permission hash. */
export function shortHash(b64: string): string {
  try {
    const bin = atob(b64);
    let hex = "";
    for (let i = 0; i < Math.min(bin.length, 8); i++) hex += bin.charCodeAt(i).toString(16).padStart(2, "0");
    return hex;
  } catch {
    return "";
  }
}

export function humanize(code: string): string {
  return code.replace(/[._]/g, " ").toLowerCase();
}
