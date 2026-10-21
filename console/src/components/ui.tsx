import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { money, outcomeLabel, reasonHelp, statusLabel } from "../format";
import type { Budget, Decision, Outcome, PermissionStatus } from "../types";

export function OutcomeBadge({ outcome }: { outcome: Outcome }) {
  const l = outcomeLabel[outcome];
  return (
    <span className={`badge tone-${l.tone}`}>
      <span className="i" aria-hidden="true">{l.icon}</span>
      {l.text}
    </span>
  );
}

export function StatusBadge({ status }: { status: PermissionStatus }) {
  const l = statusLabel[status];
  return (
    <span className={`badge tone-${l.tone}`}>
      <span className="i" aria-hidden="true">{l.icon}</span>
      {l.text}
    </span>
  );
}

export function Tone({ tone, children }: { tone: "ok" | "ask" | "stop" | "muted"; children: ReactNode }) {
  return <span className={`badge tone-${tone}`}>{children}</span>;
}

export function ReasonCodes({ reasons }: { reasons: string[] }) {
  return (
    <span className="chips">
      {reasons.map((r) => (
        <span key={r} className="code" title={reasonHelp[r] ?? r}>
          {r}
        </span>
      ))}
    </span>
  );
}

export function BudgetMeter({ budget, monthly, period }: { budget: Budget; monthly: number; period?: string }) {
  const spent = Math.min(budget.committed_cents, monthly);
  const held = Math.min(budget.held_cents, Math.max(monthly - spent, 0));
  const left = Math.max(monthly - budget.committed_cents - budget.held_cents, 0);
  const pct = (c: number) => (monthly > 0 ? `${(c / monthly) * 100}%` : "0%");
  return (
    <div className="meter">
      <div
        className="meter-bar"
        role="img"
        aria-label={`${money(budget.committed_cents)} spent and ${money(budget.held_cents)} held of ${money(monthly)}; ${money(left)} left`}
      >
        <div className="spent" style={{ width: pct(spent) }} />
        <div className="held" style={{ width: pct(held) }} />
      </div>
      <div className="meter-legend">
        <span>
          <b>{money(left)}</b> left of {money(monthly)}
          {period ? ` in ${period}` : ""}
        </span>
        <span>
          <span className="swatch spent" aria-hidden="true" />
          {money(budget.committed_cents)} spent
        </span>
        {budget.held_cents > 0 && (
          <span>
            <span className="swatch held" aria-hidden="true" />
            {money(budget.held_cents)} held
          </span>
        )}
      </div>
    </div>
  );
}

/** The one-tap approval card for a CONFIRM decision. */
export function ApprovalCard({
  decision,
  permissionName,
  onAnswer,
}: {
  decision: Decision;
  permissionName?: string;
  onAnswer: (id: string, action: "approve" | "decline") => Promise<void>;
}) {
  const [busy, setBusy] = useState<"" | "approve" | "decline">("");
  const secondsLeft = decision.reservation?.expires_in_seconds ?? 0;
  const answer = async (a: "approve" | "decline") => {
    setBusy(a);
    try {
      await onAnswer(decision.decision_id, a);
    } finally {
      setBusy("");
    }
  };
  return (
    <article className="card approval" aria-labelledby={`ap-${decision.decision_id}`}>
      <div className="card-head">
        <h3 id={`ap-${decision.decision_id}`}>
          {money(decision.amount_cents)} at {decision.merchant}
        </h3>
        <OutcomeBadge outcome="CONFIRM" />
      </div>
      <p className="why-asked">{decision.explanation}</p>
      <dl className="facts">
        <dt>Item</dt>
        <dd>{decision.category}</dd>
        <dt>Store</dt>
        <dd>{decision.merchant}</dd>
        <dt>Amount</dt>
        <dd>{money(decision.amount_cents)}</dd>
        <dt>Budget after</dt>
        <dd>{money(decision.remaining_after_cents)} left this month</dd>
        {permissionName && (
          <>
            <dt>Permission</dt>
            <dd>{permissionName}</dd>
          </>
        )}
        <dt>Hold expires</dt>
        <dd>{secondsLeft > 0 ? `in ${Math.ceil(secondsLeft / 60)} min` : "soon"}</dd>
      </dl>
      <div className="actions">
        <button className="btn primary big" disabled={!!busy} onClick={() => answer("approve")}>
          {busy === "approve" ? "Approving…" : `Approve ${money(decision.amount_cents)}`}
        </button>
        <button className="btn big" disabled={!!busy} onClick={() => answer("decline")}>
          {busy === "decline" ? "Declining…" : "Decline"}
        </button>
        <a className="btn" href={`#/d/${decision.decision_id}`}>Details</a>
      </div>
    </article>
  );
}

// Toasts
const ToastCtx = createContext<(msg: string) => void>(() => {});
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [msg, setMsg] = useState("");
  const show = useCallback((m: string) => setMsg(m), []);
  useEffect(() => {
    if (!msg) return;
    const t = window.setTimeout(() => setMsg(""), 3500);
    return () => window.clearTimeout(t);
  }, [msg]);
  return (
    <ToastCtx.Provider value={show}>
      {children}
      <div aria-live="polite" role="status">
        {msg && <div className="toast">{msg}</div>}
      </div>
    </ToastCtx.Provider>
  );
}

export function Loading({ what = "Loading" }: { what?: string }) {
  return <p className="spinner" role="status">{what}…</p>;
}

export function ErrorNote({ error }: { error: string | null }) {
  if (!error) return null;
  return <p className="alert stop" role="alert">{error}</p>;
}
