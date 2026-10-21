import { useCallback, useEffect, useState } from "react";
import { api, ApiError, getToken } from "./api";
import { Loading, ToastProvider } from "./components/ui";
import { loadPending } from "./pending";
import { Approvals } from "./pages/Approvals";
import { DecisionDetail } from "./pages/DecisionDetail";
import { Editor } from "./pages/Editor";
import { Login } from "./pages/Login";
import { PermissionDetail } from "./pages/PermissionDetail";
import { Permissions } from "./pages/Permissions";
import { Settings } from "./pages/Settings";
import { useRoute } from "./router";
import type { ServerConfig } from "./types";

export function App() {
  const route = useRoute();
  const [authed, setAuthed] = useState<boolean | null>(getToken() ? null : false);
  const [config, setConfig] = useState<ServerConfig | null>(null);
  const [pendingCount, setPendingCount] = useState(0);

  const loadConfig = useCallback(async () => {
    try {
      setConfig(await api.config());
      setAuthed(true);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) setAuthed(false);
      else setAuthed(true);
    }
  }, []);

  const refreshPending = useCallback(async () => {
    try {
      setPendingCount((await loadPending()).length);
    } catch {
      // Shown on the pages themselves.
    }
  }, []);

  useEffect(() => {
    if (authed === null) void loadConfig();
  }, [authed, loadConfig]);

  useEffect(() => {
    if (!authed) return;
    void refreshPending();
    const t = window.setInterval(() => document.visibilityState === "visible" && void refreshPending(), 5000);
    return () => window.clearInterval(t);
  }, [authed, refreshPending]);

  useEffect(() => {
    document.title = pendingCount > 0 ? `(${pendingCount}) Warrant Console` : "Warrant Console";
  }, [pendingCount]);

  const current = (page: string) => (route.page === page ? "page" : undefined);

  return (
    <ToastProvider>
      <div className="sandbox-banner">Sandbox · PayPal test money only · nothing real is charged</div>
      <header className="topbar">
        <a className="brand" href="#/">
          Warrant<small>Let agents shop. Keep the keys.</small>
        </a>
        {authed && (
          <nav className="nav" aria-label="Main">
            <a href="#/" aria-current={current("permissions") ?? current("permission") ?? current("edit")}>Permissions</a>
            <a href="#/approvals" aria-current={current("approvals")}>
              Approvals
              {pendingCount > 0 && <span className="count" aria-label={`${pendingCount} waiting`}>{pendingCount}</span>}
            </a>
            <a href="#/settings" aria-current={current("settings")}>Settings</a>
          </nav>
        )}
      </header>
      <main>
        {authed === null && <Loading />}
        {authed === false && <Login onDone={() => setAuthed(null)} />}
        {authed && (
          <>
            {route.page === "permissions" && <Permissions pendingCount={pendingCount} />}
            {route.page === "new" && <Editor config={config} />}
            {route.page === "edit" && <Editor key={route.id} id={route.id} config={config} />}
            {route.page === "permission" && <PermissionDetail id={route.id} onChanged={refreshPending} />}
            {route.page === "decision" && <DecisionDetail id={route.id} />}
            {route.page === "approvals" && <Approvals onChanged={refreshPending} />}
            {route.page === "settings" && <Settings config={config} onSignOut={() => setAuthed(false)} />}
          </>
        )}
      </main>
    </ToastProvider>
  );
}
