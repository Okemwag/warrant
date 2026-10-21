import { api } from "../api";
import { ErrorNote, Loading, OutcomeBadge, Tone } from "../components/ui";
import { humanize, money, reasonHelp, shortHash, sourceLabel, when } from "../format";
import { useLoad } from "../hooks";
import { orderText, orderTone } from "./PermissionDetail";

async function load(id: string) {
  const decision = await api.decision(id);
  const [permission, orders, events] = await Promise.all([
    api.getPermission(decision.permission_id),
    api.orders(decision.permission_id),
    api.events(decision.permission_id),
  ]);
  return {
    decision,
    permission,
    order: orders.find((o) => o.decision_id === id) ?? null,
    events: events.filter((e) => e.decision_id === id).reverse(),
  };
}

export function DecisionDetail({ id }: { id: string }) {
  const { data, error, loading } = useLoad(() => load(id), [id], 5000);
  if (loading && !data) return <Loading />;
  if (!data) return <ErrorNote error={error} />;
  const { decision: d, permission: p, order: o, events } = data;
  // The version this decision was made under.
  const v = [p.current, p.latest].find((x) => x?.permission.version === d.permission_version);

  const download = () => {
    const record = {
      kind: "warrant.decision-record",
      note: "Sandbox record. Supports a dispute; does not decide one.",
      exported_at: new Date().toISOString(),
      decision: d,
      permission: { id: p.id, name: p.name, version: d.permission_version, hash_b64: v?.hash, key_id: v?.key_id, signed_at: v?.signed_at, limits: v?.permission },
      paypal_order: o,
      events,
    };
    const url = URL.createObjectURL(new Blob([JSON.stringify(record, null, 2)], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `warrant-${d.decision_id}.json`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <>
      <div className="page-head">
        <div>
          <p className="small"><a href="#/">Permissions</a> / <a href={`#/p/${p.id}`}>{p.name}</a> /</p>
          <h1 style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap" }}>
            {money(d.amount_cents)} at {d.merchant} <OutcomeBadge outcome={d.decision} />
          </h1>
          <p>{d.explanation}</p>
        </div>
        <button className="btn" onClick={download}>Download record (JSON)</button>
      </div>

      <div className="grid-2">
        <section className="card" aria-labelledby="dec-h">
          <h2 id="dec-h" style={{ marginBottom: 12 }}>Decision</h2>
          <dl className="kv">
            <dt>Decision ID</dt><dd className="mono">{d.decision_id}</dd>
            <dt>Outcome</dt><dd>{d.decision} · {humanize(d.status)}</dd>
            <dt>Made</dt><dd>{when(d.created_at)}</dd>
            <dt>Item</dt><dd>{d.category}</dd>
            <dt>Store</dt><dd>{d.merchant}</dd>
            <dt>Amount</dt><dd>{money(d.amount_cents)}</dd>
            <dt>Source</dt><dd>{sourceLabel[d.instruction_source] ?? d.instruction_source}</dd>
            {d.human_action && <><dt>Your answer</dt><dd>{d.human_action}</dd></>}
            <dt>Budget hold</dt>
            <dd>
              {d.reservation
                ? `${money(d.reservation.amount_cents)} · ${d.reservation.status}${d.reservation.release_reason ? ` (${humanize(d.reservation.release_reason)})` : ""}`
                : "none"}
            </dd>
          </dl>
          <h3 style={{ margin: "16px 0 8px" }}>Why</h3>
          <ul className="preview-lines small">
            {d.reasons.map((r) => (
              <li key={r}><span className="code">{r}</span> {reasonHelp[r] ?? ""}</li>
            ))}
          </ul>
        </section>

        <div className="stack">
          <section className="card" aria-labelledby="perm-h">
            <h2 id="perm-h" style={{ marginBottom: 12 }}>Permission in force</h2>
            <dl className="kv">
              <dt>Permission</dt><dd>{p.name}</dd>
              <dt>Version</dt><dd>{d.permission_version ?? "not signed"}</dd>
              {v && <><dt>Hash</dt><dd className="mono">{shortHash(v.hash)}…</dd></>}
              {v?.key_id && <><dt>Signed by key</dt><dd className="mono">{v.key_id}</dd></>}
              {v?.signed_at && <><dt>Signed</dt><dd>{when(v.signed_at)}</dd></>}
            </dl>
          </section>

          <section className="card" aria-labelledby="pp-h">
            <h2 id="pp-h" style={{ marginBottom: 12 }}>PayPal</h2>
            {!o && <p className="muted">No PayPal order for this decision.</p>}
            {o && (
              <dl className="kv">
                <dt>Status</dt><dd><Tone tone={orderTone[o.status]}>{orderText[o.status]}</Tone></dd>
                <dt>Order ID</dt><dd className="mono">{o.order_id}</dd>
                <dt>Mode</dt><dd>{o.mode === "vault" ? "Saved PayPal account" : "Approved on PayPal"}</dd>
                <dt>Approved amount</dt><dd>{money(o.quoted_cents)}</dd>
                {o.authorized_cents != null && <><dt>PayPal authorized</dt><dd>{money(o.authorized_cents)}</dd></>}
                {o.authorization_id && <><dt>Authorization</dt><dd className="mono">{o.authorization_id}</dd></>}
                {o.capture_id && <><dt>Capture</dt><dd className="mono">{o.capture_id}</dd></>}
                {o.refund_id && <><dt>Refund</dt><dd className="mono">{o.refund_id}</dd></>}
                {o.failure && <><dt>Note</dt><dd>{o.failure}</dd></>}
                {o.debug_id && <><dt>PayPal debug ID</dt><dd className="mono">{o.debug_id}</dd></>}
                <dt>Invoice</dt><dd className="mono">{o.invoice_id}</dd>
              </dl>
            )}
          </section>
        </div>
      </div>

      <section className="card" style={{ marginTop: 20 }} aria-labelledby="trail-h">
        <h2 id="trail-h" style={{ marginBottom: 8 }}>Trail</h2>
        <ol className="timeline">
          {events.map((e) => (
            <li key={e.id}>
              <span className="muted small">{when(e.at)}</span>
              <span>{humanize(e.type)}{typeof e.data.reason === "string" ? ` · ${humanize(e.data.reason)}` : ""}</span>
            </li>
          ))}
        </ol>
        <p className="muted small" style={{ marginTop: 12 }}>
          This record supports a dispute; it doesn't decide one. A full evidence pack (PDF, attached to PayPal disputes) comes in Phase 6.
        </p>
      </section>
    </>
  );
}
