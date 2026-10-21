import { useEffect, useMemo, useState, type FormEvent, type KeyboardEvent } from "react";
import { api, ApiError } from "../api";
import { ErrorNote, Loading, OutcomeBadge, useToast } from "../components/ui";
import { dollarsInput, money, parseDollars, sourceLabel } from "../format";
import { useDebounced } from "../hooks";
import { go } from "../router";
import type { Draft, Permission, PermissionInput, Preview, ServerConfig } from "../types";

interface Form {
  name: string;
  monthly: string;
  perPurchase: string;
  confirmAbove: string;
  categories: string[];
  merchants: string[];
  askNew: boolean;
  minReturn: string;
  days: string;
}

const TEMPLATES: { id: string; title: string; blurb: string; form: Form }[] = [
  {
    id: "errands",
    title: "Everyday errands",
    blurb: "$100 a month, $40 max, asks above $25, 14-day returns.",
    form: { name: "Everyday errands", monthly: "100", perPurchase: "40", confirmAbove: "25", categories: ["household", "groceries"], merchants: ["demo-store"], askNew: true, minReturn: "14", days: "90" },
  },
  {
    id: "groceries",
    title: "Weekly groceries",
    blurb: "$300 a month, any store, asks above $80.",
    form: { name: "Weekly groceries", monthly: "300", perPurchase: "120", confirmAbove: "80", categories: ["groceries"], merchants: [], askNew: true, minReturn: "0", days: "90" },
  },
  {
    id: "running",
    title: "Running gear",
    blurb: "$120 a month, $60 max, 30-day returns, demo store only.",
    form: { name: "Running gear", monthly: "120", perPurchase: "60", confirmAbove: "25", categories: ["running gear"], merchants: ["demo-store"], askNew: false, minReturn: "30", days: "30" },
  },
];

function formFrom(p: PermissionInput & { starts_at?: string; expires_at?: string }): Form {
  const days =
    p.starts_at && p.expires_at
      ? Math.max(1, Math.round((Date.parse(p.expires_at) - Date.parse(p.starts_at)) / 86400000))
      : 90;
  return {
    name: p.name,
    monthly: dollarsInput(p.monthly_budget_cents),
    perPurchase: dollarsInput(p.per_purchase_cap_cents),
    confirmAbove: dollarsInput(p.confirm_above_cents),
    categories: [...p.categories],
    merchants: [...p.merchants],
    askNew: p.ask_on_new_merchant,
    minReturn: String(p.min_return_days),
    days: String(days),
  };
}

function toInput(f: Form, startsAt?: string): { input?: PermissionInput; problems: string[] } {
  const problems: string[] = [];
  const m = parseDollars(f.monthly);
  const pp = parseDollars(f.perPurchase);
  const ca = parseDollars(f.confirmAbove);
  if (m === null) problems.push("Monthly budget must be an amount like 100 or 100.50.");
  if (pp === null) problems.push("Per-purchase limit must be an amount.");
  if (ca === null) problems.push("Approval line must be an amount.");
  const minReturn = Number(f.minReturn);
  const days = Number(f.days);
  if (!Number.isInteger(minReturn) || minReturn < 0) problems.push("Minimum return window must be a whole number of days.");
  if (!Number.isInteger(days) || days < 1 || days > 366) problems.push("Duration must be 1 to 366 days.");
  if (problems.length) return { problems };
  const start = startsAt ? new Date(startsAt) : new Date();
  return {
    problems,
    input: {
      name: f.name.trim(),
      monthly_budget_cents: m!,
      per_purchase_cap_cents: pp!,
      confirm_above_cents: ca!,
      categories: f.categories,
      merchants: f.merchants,
      ask_on_new_merchant: f.askNew,
      min_return_days: minReturn,
      currency: "USD",
      starts_at: start.toISOString(),
      expires_at: new Date(start.getTime() + days * 86400000).toISOString(),
    },
  };
}

export function Editor({ id, config }: { id?: string; config: ServerConfig | null }) {
  const toast = useToast();
  const [existing, setExisting] = useState<Permission | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [template, setTemplate] = useState<string>(id ? "" : "errands");
  const [form, setForm] = useState<Form>(TEMPLATES[0].form);
  const [dirty, setDirty] = useState(!id);
  const [text, setText] = useState("");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [compiling, setCompiling] = useState(false);
  const [compileError, setCompileError] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [signing, setSigning] = useState(false);
  const [signError, setSignError] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    api.getPermission(id).then(
      (p) => {
        setExisting(p);
        setForm(formFrom(p.latest.permission));
      },
      (e) => setLoadError(String(e instanceof Error ? e.message : e)),
    );
  }, [id]);

  const update = (patch: Partial<Form>) => {
    setForm((f) => ({ ...f, ...patch }));
    setDirty(true);
  };

  const built = useMemo(() => toInput(form, existing?.latest.permission.starts_at), [form, existing]);
  const debounced = useDebounced(built.input ? JSON.stringify(built.input) : "", 300);
  useEffect(() => {
    if (!debounced) return;
    let live = true;
    api.preview(JSON.parse(debounced)).then((p) => live && setPreview(p), () => {});
    return () => {
      live = false;
    };
  }, [debounced]);

  const compile = async (e: FormEvent) => {
    e.preventDefault();
    setCompiling(true);
    setCompileError("");
    try {
      const res = await api.draft(text);
      setDraft(res.draft);
      setForm(formFrom(res.draft.permission));
      setTemplate("");
      setDirty(true);
    } catch (err) {
      setCompileError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setCompiling(false);
    }
  };

  const sign = async () => {
    if (!built.input) return;
    setSigning(true);
    setSignError(null);
    try {
      let p: Permission;
      if (!existing) {
        p = await api.createPermission(built.input);
        p = await api.sign(p.id, p.latest_version);
      } else if (!dirty && existing.current_version !== existing.latest_version) {
        p = await api.sign(existing.id, existing.latest_version);
      } else {
        p = await api.newVersion(existing.id, built.input);
        p = await api.sign(p.id, p.latest_version);
      }
      toast(`Signed. "${p.name}" is ${p.status}.`);
      go({ page: "permission", id: p.id });
    } catch (err) {
      setSignError(err instanceof Error ? err.message : String(err));
    } finally {
      setSigning(false);
    }
  };

  if (id && !existing && !loadError) return <Loading />;

  const canSign = !!built.input && !!preview?.valid && !signing && (dirty || existing?.current_version !== existing?.latest_version);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{existing ? `Edit “${existing.name}”` : "New permission"}</h1>
          <p>
            {existing
              ? "Changes become a new version. Nothing changes for your agent until you sign it."
              : "Pick a starting point, adjust the limits, see how it decides, then sign."}
          </p>
        </div>
      </div>
      <ErrorNote error={loadError} />

      {!existing && (
        <section className="card flat" style={{ marginBottom: 20 }} aria-labelledby="start-h">
          <h2 id="start-h" style={{ marginBottom: 12 }}>1. Start from</h2>
          <div className="templates">
            {TEMPLATES.map((t) => (
              <button
                key={t.id}
                type="button"
                className="template"
                aria-pressed={template === t.id}
                onClick={() => {
                  setTemplate(t.id);
                  setForm(t.form);
                  setDraft(null);
                }}
              >
                <b>{t.title}</b>
                <span className="muted small">{t.blurb}</span>
              </button>
            ))}
          </div>
          <form onSubmit={compile} className="fields" style={{ marginTop: 16 }}>
            <label className="field">
              …or describe it in your own words
              <span className="hint">
                For example: “Up to $120 a month on running gear, nothing over $60, ask me above $25, only stores with 30-day returns.”
              </span>
              <textarea value={text} onChange={(e) => setText(e.target.value)} maxLength={2000} disabled={!config?.compiler_configured} />
            </label>
            {!config?.compiler_configured && (
              <p className="muted small">Plain-language setup needs ANTHROPIC_API_KEY on the server. Templates work without it.</p>
            )}
            {compileError && <p className="error-text" role="alert">{compileError}</p>}
            <div className="actions">
              <button className="btn" disabled={!config?.compiler_configured || compiling || !text.trim()}>
                {compiling ? "Reading your request…" : "Turn into limits"}
              </button>
            </div>
          </form>
        </section>
      )}

      <div className="grid-2">
        <section className="card" aria-labelledby="limits-h">
          <h2 id="limits-h" style={{ marginBottom: 14 }}>{existing ? "Limits" : "2. Adjust the limits"}</h2>
          {draft && (draft.question || draft.assumptions.length > 0) && (
            <div className="alert ask" style={{ marginBottom: 14 }}>
              {draft.question && <p style={{ fontWeight: 700, marginBottom: 6 }}>{draft.question}</p>}
              {draft.assumptions.length > 0 && (
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {draft.assumptions.map((a) => <li key={a}>{a}</li>)}
                </ul>
              )}
            </div>
          )}
          <div className="fields">
            <label className="field">
              Name
              <input type="text" value={form.name} onChange={(e) => update({ name: e.target.value })} maxLength={80} />
            </label>
            <div className="fields-3">
              <MoneyField label="Monthly budget" hint="Hard cap" value={form.monthly} onChange={(v) => update({ monthly: v })} />
              <MoneyField label="Per purchase" hint="Hard cap" value={form.perPurchase} onChange={(v) => update({ perPurchase: v })} />
              <MoneyField label="Ask me above" hint="Approval line" value={form.confirmAbove} onChange={(v) => update({ confirmAbove: v })} />
            </div>
            <ListField
              label="What it can buy"
              hint="Categories, e.g. household"
              items={form.categories}
              onChange={(categories) => update({ categories })}
            />
            <ListField
              label="Stores"
              hint={`Leave empty for any store. Known store: ${config?.merchants.map((m) => m.id).join(", ") || "demo-store"}`}
              items={form.merchants}
              onChange={(merchants) => update({ merchants })}
            />
            {form.merchants.length > 0 && (
              <label className="check">
                <input type="checkbox" checked={form.askNew} onChange={(e) => update({ askNew: e.target.checked })} />
                <span>Ask me before using a store that isn't listed (otherwise block it)</span>
              </label>
            )}
            <div className="fields-3">
              <label className="field">
                Minimum returns
                <span className="hint">Days. 0 = don't check</span>
                <input type="number" min={0} max={365} value={form.minReturn} onChange={(e) => update({ minReturn: e.target.value })} />
              </label>
              <label className="field">
                Lasts
                <span className="hint">Days from now</span>
                <input type="number" min={1} max={366} value={form.days} onChange={(e) => update({ days: e.target.value })} />
              </label>
            </div>
            {built.problems.map((p) => <p key={p} className="error-text">{p}</p>)}
          </div>
        </section>

        <section className="card" aria-labelledby="preview-h" aria-live="polite">
          <h2 id="preview-h" style={{ marginBottom: 12 }}>{existing ? "What you're signing" : "3. Read it, try it, sign it"}</h2>
          {!preview && <Loading what="Preparing preview" />}
          {preview && (
            <>
              <ul className="preview-lines">
                {preview.summary.map((l) => <li key={l}>{l}</li>)}
              </ul>
              {preview.errors.length > 0 && (
                <div className="alert stop" style={{ marginTop: 12 }}>
                  {preview.errors.map((e) => <p key={e}>{capitalize(e)}</p>)}
                </div>
              )}
              {preview.valid && (
                <>
                  <h3 style={{ margin: "18px 0 4px" }}>Try it: three purchases your agent might propose</h3>
                  <div>
                    {preview.trials.map((t) => (
                      <div className="trial" key={t.label}>
                        <OutcomeBadge outcome={t.decision} />
                        <span className="what">
                          {money(t.amount_cents)} of {t.category} at {t.merchant}
                          <span className="muted small"> · {sourceLabel[t.instruction_source] ?? t.instruction_source}</span>
                        </span>
                        <span className="why">{t.explanation}</span>
                      </div>
                    ))}
                  </div>
                </>
              )}
              <hr style={{ border: 0, borderTop: "1px solid var(--line)", margin: "16px 0" }} />
              <p className="muted small" style={{ marginBottom: 12 }}>
                Signing makes these limits binding: Warrant checks every purchase your agent proposes against exactly this version.
              </p>
              <ErrorNote error={signError} />
              <div className="actions">
                <button className="btn primary big" disabled={!canSign} onClick={sign}>
                  {signing ? "Signing…" : existing ? "Sign this version" : "Sign and activate"}
                </button>
                <a className="btn big" href={existing ? `#/p/${existing.id}` : "#/"}>Cancel</a>
              </div>
            </>
          )}
        </section>
      </div>
    </>
  );
}

function MoneyField({ label, hint, value, onChange }: { label: string; hint: string; value: string; onChange: (v: string) => void }) {
  const invalid = parseDollars(value) === null;
  return (
    <label className="field">
      {label}
      <span className="hint">{hint}</span>
      <span className="money-input">
        <span aria-hidden="true">$</span>
        <input type="text" inputMode="decimal" value={value} aria-invalid={invalid} onChange={(e) => onChange(e.target.value)} />
      </span>
    </label>
  );
}

function ListField({ label, hint, items, onChange }: { label: string; hint: string; items: string[]; onChange: (v: string[]) => void }) {
  const [entry, setEntry] = useState("");
  const add = () => {
    const v = entry.trim().toLowerCase();
    if (v && !items.includes(v)) onChange([...items, v]);
    setEntry("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add();
    }
  };
  return (
    <div className="field" role="group" aria-label={label}>
      <span style={{ fontWeight: 600, fontSize: 14 }}>{label}</span>
      <span className="hint">{hint}</span>
      {items.length > 0 && (
        <div className="chips">
          {items.map((i) => (
            <span className="chip" key={i}>
              {i}
              <button type="button" aria-label={`Remove ${i}`} onClick={() => onChange(items.filter((x) => x !== i))}>×</button>
            </span>
          ))}
        </div>
      )}
      <div className="actions">
        <input type="text" value={entry} onChange={(e) => setEntry(e.target.value)} onKeyDown={onKey} aria-label={`Add to ${label}`} style={{ flex: "1 1 160px", width: "auto" }} />
        <button type="button" className="btn" onClick={add} disabled={!entry.trim()}>Add</button>
      </div>
    </div>
  );
}

function capitalize(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
