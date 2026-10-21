import { useState, type FormEvent } from "react";
import { api, ApiError, setToken } from "../api";

export function Login({ onDone }: { onDone: () => void }) {
  const [token, setTok] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setToken(token.trim());
    try {
      await api.config();
      onDone();
    } catch (err) {
      setToken("");
      setError(err instanceof ApiError && err.status === 401 ? "That token isn't the console token." : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card login">
      <h1>Sign in to Warrant</h1>
      <p className="muted" style={{ margin: "6px 0 18px" }}>
        Paste the console token (<span className="mono">WARRANT_CONSOLE_TOKEN</span> in your <span className="mono">.env</span>).
        It stays in this browser tab only.
      </p>
      <form onSubmit={submit} className="fields">
        <label className="field">
          Console token
          <input type="password" autoComplete="current-password" value={token} onChange={(e) => setTok(e.target.value)} required autoFocus />
        </label>
        {error && <p className="error-text" role="alert">{error}</p>}
        <button className="btn primary big" disabled={busy || !token.trim()}>
          {busy ? "Checking…" : "Continue"}
        </button>
      </form>
    </div>
  );
}
