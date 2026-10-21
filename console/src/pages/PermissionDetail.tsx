import { api } from "../api";
import { ApprovalCard, BudgetMeter, ErrorNote, Loading, OutcomeBadge, ReasonCodes, StatusBadge, Tone, useToast } from "../components/ui";
import { day, humanize, money, shortHash, sourceLabel, when } from "../format";
import { useLoad } from "../hooks";
import type { Decision, EnginePermission, Order, Permission, PermissionInput } from "../types";

function inputOf(p: EnginePermission): PermissionInput {
  return {
    name: p.name, monthly_budget_cents: p.monthly_budget_cents, per_purchase_cap_cents: p.per_purchase_cap_cents,
    confirm_above_cents: p.confirm_above_cents, categories: p.categories, merchants: p.merchants,
    ask_on_new_merchant: p.ask_on_new_merchant, min_return_days: p.min_return_days, currency: p.currency,
    starts_at: p.starts_at, expires_at: p.expires_at,
  };
}

interface Bundle {
  permission: Permission;
  decisions: Decision[];
  orders: Order[];
  summary: string[];
}

async function load(id: string): Promise<Bundle> {
  const permission = await api.getPermission(id);
  const v = permission.current ?? permission.latest;
  const [decisions, orders, preview] = await Promise.all([
    api.decisions(id),
    api.orders(id),
    api.preview(inputOf(v.permission)),
  ]);
  return { permission, decisions, orders, summary: preview.summary };
}

export const orderTone: Record<Order["status"], "ok" | "ask" | "stop" | "muted"> = {
  completed: "ok",
  captured: "ask",
  authorized: "ask",
  awaiting_buyer: "ask",
  refunded: "muted",
  voided: "muted",
  failed: "stop",
};

export const orderText: Record<Order["status"], string> = {
  completed: "Paid",
  captured: "Payment pending",
  authorized: "Authorized",
  awaiting_buyer: "Waiting for PayPal approval",
  refunded: "Refunded",
  voided: "Voided",
  failed: "Payment failed",
};

export function PermissionDetail({ id, onChanged }: { id: string; onChanged: () => void }) {
  const { data, error, loading, reload } = useLoad(() => load(id), [id], 5000);
  const toast = useToast();

  if (loading && !data) return <Loading />;
  if (!data) return <ErrorNote error={error} />;
  const { permission: p, decisions, orders, summary } = data;
  const v = p.current ?? p.latest;
  const orderBy = new Map(orders.map((o) => [o.decision_id, o]));
  const pending = decisions.filter((d) => d.status === "awaiting_approval" && (d.reservation?.expires_in_seconds ?? 0) > 0);

  const setStatus = async (status: "active" | "paused" | "revoked") => {
    if (status === "revoked" && !window.confirm(`Revoke "${p.name}"? Your agent can never use it again, and any held budget is released.`)) return;
    try {
      await api.setStatus(p.id, status);
      toast(status === "paused" ? "Paused. Holds released." : status === "revoked" ? "Revoked." : "Resumed.");
      await reload();
      onChanged();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  };

  const answer = async (decisionId: string, action: "approve" | "decline") => {
    try {
      await api.answer(decisionId, action);
      toast(action === "approve" ? "Approved. Your agent can check out." : "Declined. The hold was released.");
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
    await reload();
    onChanged();
  };

  const refund = async (o: Order) => {
    if (!window.confirm(`Refund ${money(o.final_cents)} through PayPal (sandbox)?`)) return;
    try {
      await api.refund(o.order_id);
      toast("Refunded. The amount is back in this month's budget.");
      await reload();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <p className="small"><a href="#/">Permissions</a> /</p>
          <h1 style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap" }}>
            {p.name} <StatusBadge status={p.status} />
          </h1>
          <p>
            Version {p.current_version ?? "—"}
            {p.current?.hash && <> · hash <span className="mono">{shortHash(p.current.hash)}</span></>}
            {p.current?.signed_at && <> · signed {when(p.current.signed_at)}</>}
            {p.latest_version !== p.current_version && " · an unsigned edit is waiting"}
          </p>
        </div>
        <div className="actions">
          {p.status !== "revoked" && <a className="btn" href={`#/p/${p.id}/edit`}>{p.status === "draft" ? "Review and sign" : "Edit"}</a>}
          {p.status === "active" && <button className="btn" onClick={() => setStatus("paused")}>Pause</button>}
          {p.status === "paused" && <button className="btn" onClick={() => setStatus("active")}>Resume</button>}
          {p.status !== "revoked" && <button className="btn danger" onClick={() => setStatus("revoked")}>Revoke</button>}
        </div>
      </div>
      <ErrorNote error={error} />

      <div className="grid-2" style={{ marginBottom: 20 }}>
        <section className="card" aria-labelledby="budget-h">
          <h2 id="budget-h" style={{ marginBottom: 12 }}>This month</h2>
          <BudgetMeter budget={p.budget} monthly={v.permission.monthly_budget_cents} period={p.period} />
        </section>
        <section className="card" aria-labelledby="limits-h">
          <h2 id="limits-h" style={{ marginBottom: 8 }}>The limits</h2>
          <ul className="preview-lines small">
            {summary.map((l) => <li key={l}>{l}</li>)}
          </ul>
        </section>
      </div>

      {pending.length > 0 && (
        <section className="stack" style={{ marginBottom: 20 }} aria-labelledby="pending-h">
          <h2 id="pending-h">Waiting for you</h2>
          {pending.map((d) => <ApprovalCard key={d.decision_id} decision={d} onAnswer={answer} />)}
        </section>
      )}

      <section className="card" aria-labelledby="activity-h" style={{ marginBottom: 20 }}>
        <h2 id="activity-h" style={{ marginBottom: 4 }}>Activity</h2>
        <p className="muted small" style={{ marginBottom: 8 }}>Every purchase your agent proposed, what Warrant decided, and why.</p>
        {decisions.length === 0 && (
          <p className="empty">No purchases yet. Give your agent the permission ID below and it will show up here.</p>
        )}
        <div className="rows">
          {decisions.map((d) => {
            const o = orderBy.get(d.decision_id);
            return (
              <div className="row" key={d.decision_id}>
                <div style={{ display: "grid", gap: 4, minWidth: 0 }}>
                  <span className="title">
                    <OutcomeBadge outcome={d.decision} />
                    <a className="title-link" href={`#/d/${d.decision_id}`}>{d.category} at {d.merchant}</a>
                    {d.human_action && <Tone tone={d.human_action === "approved" ? "ok" : "muted"}>You {d.human_action}</Tone>}
                    {o && <Tone tone={orderTone[o.status]}>{orderText[o.status]}</Tone>}
                    {!o && d.status === "released" && d.decision !== "BLOCK" && <Tone tone="muted">Hold {d.reservation?.release_reason ? humanize(d.reservation.release_reason) : "released"}</Tone>}
                  </span>
                  <span>{d.explanation}</span>
                  <span className="meta">
                    <span>{when(d.created_at)}</span>
                    <span>source: {sourceLabel[d.instruction_source] ?? d.instruction_source}</span>
                    <ReasonCodes reasons={d.reasons} />
                  </span>
                </div>
                <div style={{ display: "grid", gap: 6, justifyItems: "end" }}>
                  <span className="amount">{money(d.amount_cents)}</span>
                  {o?.status === "completed" && <button className="btn" onClick={() => refund(o)}>Refund</button>}
                  {o?.status === "awaiting_buyer" && o.approve_url && (
                    <a className="btn paypal" href={o.approve_url} target="_blank" rel="noreferrer">Approve on PayPal</a>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      </section>

      <section className="card flat" aria-labelledby="agent-h">
        <h2 id="agent-h" style={{ marginBottom: 8 }}>Connect your agent</h2>
        <p className="muted small" style={{ marginBottom: 10 }}>
          Your agent uses this permission ID with Warrant's <b>evaluate</b> and <b>checkout</b> tools. It never gets PayPal access.
        </p>
        <dl className="kv">
          <dt>Permission ID</dt>
          <dd className="mono">{p.id}</dd>
          <dt>Valid</dt>
          <dd>{day(v.permission.starts_at)} – {day(v.permission.expires_at)}</dd>
        </dl>
      </section>
    </>
  );
}
