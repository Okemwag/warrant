import { useState } from "react";
import { api, setToken } from "../api";
import { ErrorNote, Loading, Tone, useToast } from "../components/ui";
import { useLoad } from "../hooks";
import type { ServerConfig } from "../types";

export function Settings({ config, onSignOut }: { config: ServerConfig | null; onSignOut: () => void }) {
  const { data: conn, error, loading, reload } = useLoad(() => api.connection(), [], 0);
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<unknown>, done?: string) => {
    setBusy(true);
    try {
      await fn();
      if (done) toast(done);
      await reload();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const connect = () =>
    run(async () => {
      const c = await api.connect();
      if (c.approve_url) window.open(c.approve_url, "_blank", "noopener");
    });

  const mcpConfig = JSON.stringify(
    {
      mcpServers: {
        warrant: {
          command: "warrant-mcp",
          env: { WARRANT_API_URL: window.location.origin, WARRANT_AGENT_TOKEN: "<WARRANT_AGENT_TOKEN>" },
        },
      },
    },
    null,
    2,
  );

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <p>Payments, your agent's connection, and this session.</p>
        </div>
      </div>
      <div className="stack">
        <section className="card" aria-labelledby="pp-h">
          <div className="card-head">
            <h2 id="pp-h">PayPal (sandbox)</h2>
            {config?.paypal_configured ? <Tone tone="ok">Configured</Tone> : <Tone tone="stop">Not configured</Tone>}
          </div>
          {!config?.paypal_configured && <p className="muted">Set PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET on the server to enable checkout.</p>}
          {config?.paypal_configured && (
            <>
              <p style={{ marginBottom: 12 }}>
                Payment mode: <b>{config.paypal_mode === "vault" ? "Saved PayPal account (Mode B)" : "Approve each order on PayPal (Mode A)"}</b>
              </p>
              <h3 style={{ marginBottom: 6 }}>Saved PayPal account</h3>
              <p className="muted small" style={{ marginBottom: 10 }}>
                Connect once so purchases Warrant allows can be paid without a PayPal page each time. Only used when the server runs in Mode B.
              </p>
              <ErrorNote error={error} />
              {loading && <Loading />}
              {conn && (
                <div className="actions">
                  {conn.status === "connected" && (
                    <>
                      <Tone tone="ok">Connected {conn.vault_hint}</Tone>
                      <button className="btn danger" disabled={busy} onClick={() => run(() => api.disconnect(), "Disconnected.")}>Disconnect</button>
                    </>
                  )}
                  {conn.status === "pending" && (
                    <>
                      <Tone tone="ask">Waiting for your PayPal approval</Tone>
                      {conn.approve_url && <a className="btn paypal" href={conn.approve_url} target="_blank" rel="noreferrer">Open PayPal</a>}
                      <button className="btn" disabled={busy} onClick={() => run(() => api.finishConnect())}>I've approved it</button>
                    </>
                  )}
                  {(conn.status === "none" || conn.status === "failed") && (
                    <button className="btn paypal" disabled={busy} onClick={connect}>Connect PayPal</button>
                  )}
                </div>
              )}
            </>
          )}
        </section>

        <section className="card" aria-labelledby="agent-h">
          <h2 id="agent-h" style={{ marginBottom: 8 }}>Your agent</h2>
          <p className="muted small" style={{ marginBottom: 10 }}>
            Add Warrant's MCP server to your agent. It gets two tools, <b>evaluate</b> and <b>checkout</b>, and never sees PayPal credentials.
          </p>
          <pre className="mono card flat" style={{ margin: 0, overflowX: "auto", whiteSpace: "pre" }}>{mcpConfig}</pre>
        </section>

        <section className="card" aria-labelledby="ai-h">
          <div className="card-head">
            <h2 id="ai-h">Plain-language setup</h2>
            {config?.compiler_configured ? <Tone tone="ok">On</Tone> : <Tone tone="muted">Off</Tone>}
          </div>
          <p className="muted small">
            Uses Claude to turn your words into draft limits that you review before signing. Claude never decides purchases.
            {!config?.compiler_configured && " Set ANTHROPIC_API_KEY on the server to turn it on."}
          </p>
        </section>

        <section className="card flat">
          <button
            className="btn"
            onClick={() => {
              setToken("");
              onSignOut();
            }}
          >
            Sign out of this tab
          </button>
        </section>
      </div>
    </>
  );
}
