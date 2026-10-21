import { api } from "../api";
import { BudgetMeter, ErrorNote, Loading, StatusBadge, useToast } from "../components/ui";
import { day, money } from "../format";
import { useLoad } from "../hooks";
import type { Permission } from "../types";

export function Permissions({ pendingCount }: { pendingCount: number }) {
  const { data, error, loading, reload } = useLoad(() => api.listPermissions(), [], 10000);
  const toast = useToast();

  const setStatus = async (p: Permission, status: "active" | "paused" | "revoked") => {
    if (status === "revoked" && !window.confirm(`Revoke "${p.name}"? Your agent can never use it again, and any held budget is released.`)) return;
    try {
      await api.setStatus(p.id, status);
      toast(status === "paused" ? "Paused. Holds released." : status === "revoked" ? "Revoked." : "Resumed.");
      await reload();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Permissions</h1>
          <p>The spending limits your shopping agent works within. Pause or revoke any of them at any time.</p>
        </div>
        <a className="btn primary big" href="#/new">New permission</a>
      </div>

      {pendingCount > 0 && (
        <p className="alert ask" style={{ marginBottom: 16 }}>
          <b>{pendingCount} purchase{pendingCount === 1 ? " is" : "s are"} waiting for you.</b>{" "}
          <a href="#/approvals">Review now</a>
        </p>
      )}

      <ErrorNote error={error} />
      {loading && !data && <Loading />}
      {data && data.length === 0 && (
        <div className="card empty">
          <h2>No permissions yet</h2>
          <p style={{ marginBottom: 16 }}>Start from a template. It takes about two minutes, and you'll see how it decides before you sign.</p>
          <a className="btn primary big" href="#/new">Create your first permission</a>
        </div>
      )}
      <div className="stack">
        {data?.map((p) => {
          const v = p.current ?? p.latest;
          const lim = v.permission;
          return (
            <article className="card" key={p.id}>
              <div className="card-head">
                <h2>
                  <a href={`#/p/${p.id}`} style={{ color: "inherit", textDecoration: "none" }}>{p.name}</a>
                </h2>
                <StatusBadge status={p.status} />
              </div>
              <p className="muted small" style={{ marginBottom: 12 }}>
                Up to {money(lim.per_purchase_cap_cents)} a purchase · asks above {money(lim.confirm_above_cents)} ·{" "}
                {lim.categories.join(", ")} · until {day(lim.expires_at)}
                {p.latest_version !== p.current_version && p.status !== "draft" && " · unsigned edit pending"}
              </p>
              {p.status !== "draft" && <BudgetMeter budget={p.budget} monthly={lim.monthly_budget_cents} period={p.period} />}
              <div className="actions" style={{ marginTop: 14 }}>
                <a className="btn" href={`#/p/${p.id}`}>Activity</a>
                {p.status === "draft" && <a className="btn primary" href={`#/p/${p.id}/edit`}>Review and sign</a>}
                {p.status === "active" && <button className="btn" onClick={() => setStatus(p, "paused")}>Pause</button>}
                {p.status === "paused" && <button className="btn" onClick={() => setStatus(p, "active")}>Resume</button>}
                {p.status !== "revoked" && <button className="btn danger" onClick={() => setStatus(p, "revoked")}>Revoke</button>}
              </div>
            </article>
          );
        })}
      </div>
    </>
  );
}
