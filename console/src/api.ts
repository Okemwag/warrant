import type {
  Connection, Decision, Draft, Order, Permission, PermissionInput, PermissionStatus,
  Preview, ServerConfig, TimelineEvent,
} from "./types";

const TOKEN_KEY = "warrant.console-token";

export function getToken(): string {
  try {
    return sessionStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setToken(token: string) {
  try {
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
    else sessionStorage.removeItem(TOKEN_KEY);
  } catch {
    // Storage blocked: the token lives only for this page load.
  }
  memoryToken = token;
}

let memoryToken = "";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const token = getToken() || memoryToken;
  const res = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    throw new ApiError(res.status, "bad_response", `Unexpected response (${res.status})`);
  }
  if (!res.ok) {
    const err = (data as { error?: { code: string; message: string } })?.error;
    throw new ApiError(res.status, err?.code ?? "error", err?.message ?? `Request failed (${res.status})`);
  }
  return data as T;
}

export const api = {
  config: () => request<ServerConfig>("GET", "/v1/config"),

  listPermissions: () => request<{ permissions: Permission[] }>("GET", "/v1/permissions").then((r) => r.permissions),
  getPermission: (id: string) => request<Permission>("GET", `/v1/permissions/${id}`),
  createPermission: (p: PermissionInput) => request<Permission>("POST", "/v1/permissions", p),
  newVersion: (id: string, p: PermissionInput) => request<Permission>("POST", `/v1/permissions/${id}/versions`, p),
  sign: (id: string, version: number) => request<Permission>("POST", `/v1/permissions/${id}/sign`, { version }),
  setStatus: (id: string, status: Exclude<PermissionStatus, "draft">) =>
    request<Permission>("PATCH", `/v1/permissions/${id}`, { status }),
  preview: (p: PermissionInput) => request<Preview>("POST", "/v1/permissions/preview", { permission: p }),
  draft: (text: string) => request<{ draft: Draft; preview: Preview }>("POST", "/v1/permissions/draft", { text }),

  decisions: (id: string) =>
    request<{ decisions: Decision[] }>("GET", `/v1/permissions/${id}/decisions?limit=100`).then((r) => r.decisions),
  events: (id: string) =>
    request<{ events: TimelineEvent[] }>("GET", `/v1/permissions/${id}/events?limit=200`).then((r) => r.events),
  orders: (id: string) => request<{ orders: Order[] }>("GET", `/v1/permissions/${id}/orders`).then((r) => r.orders),
  decision: (id: string) => request<Decision>("GET", `/v1/decisions/${id}`),
  answer: (id: string, action: "approve" | "decline") =>
    request<Decision>("POST", `/v1/decisions/${id}/approve`, { action }),
  refund: (orderId: string) => request<Order>("POST", `/v1/orders/${orderId}/refund`),

  connection: () => request<Connection>("GET", "/v1/paypal/connection"),
  connect: () => request<Connection>("POST", "/v1/paypal/connect"),
  finishConnect: () => request<Connection>("POST", "/v1/paypal/connect/complete"),
  disconnect: () => request<Connection>("DELETE", "/v1/paypal/connection"),
};
