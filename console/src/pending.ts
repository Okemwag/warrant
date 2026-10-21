import { api } from "./api";
import type { Decision, Permission } from "./types";

export interface Pending {
  decision: Decision;
  permission: Permission;
}

/** Every purchase waiting for the person's answer, newest first. */
export async function loadPending(): Promise<Pending[]> {
  const perms = await api.listPermissions();
  const active = perms.filter((p) => p.status === "active");
  const lists = await Promise.all(active.map((p) => api.decisions(p.id).then((ds) => ds.map((d) => ({ decision: d, permission: p })))));
  return lists
    .flat()
    .filter((x) => x.decision.status === "awaiting_approval" && (x.decision.reservation?.expires_in_seconds ?? 0) > 0)
    .sort((a, b) => b.decision.created_at.localeCompare(a.decision.created_at));
}
