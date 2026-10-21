// Shapes returned by the Warrant API (see Architecture.md § 10).

export type Outcome = "ALLOW" | "CONFIRM" | "BLOCK";
export type PermissionStatus = "draft" | "active" | "paused" | "revoked";

export interface PermissionInput {
  name: string;
  monthly_budget_cents: number;
  per_purchase_cap_cents: number;
  confirm_above_cents: number;
  categories: string[];
  merchants: string[];
  ask_on_new_merchant: boolean;
  min_return_days: number;
  currency?: string;
  starts_at?: string;
  expires_at?: string;
}

export interface EnginePermission extends PermissionInput {
  id: string;
  version: number;
  starts_at: string;
  expires_at: string;
}

export interface Version {
  permission: EnginePermission;
  hash: string; // base64
  signature?: string;
  key_id?: string;
  signed_at?: string;
}

export interface Budget {
  committed_cents: number;
  held_cents: number;
}

export interface Permission {
  id: string;
  name: string;
  status: PermissionStatus;
  current_version: number | null;
  latest_version: number;
  current?: Version;
  latest: Version;
  period: string;
  budget: Budget;
  remaining_cents: number;
  created_at: string;
  updated_at: string;
}

export interface Reservation {
  id: string;
  status: "held" | "committed" | "released" | "refunded";
  amount_cents: number;
  expires_at: string;
  expires_in_seconds: number;
  release_reason?: string;
}

export type DecisionStatus = "ready" | "awaiting_approval" | "blocked" | "released" | "completed";

export interface Decision {
  decision_id: string;
  decision: Outcome;
  status: DecisionStatus;
  reasons: string[];
  explanation: string;
  permission_id: string;
  permission_version: number | null;
  merchant: string;
  category: string;
  amount_cents: number;
  remaining_after_cents: number;
  instruction_source: string;
  human_action?: "approved" | "declined";
  reservation: Reservation | null;
  created_at: string;
}

export interface Order {
  order_id: string;
  decision_id: string;
  reservation_id: string;
  permission_id: string;
  mode: "approve" | "vault";
  invoice_id: string;
  quoted_cents: number;
  final_cents: number;
  authorized_cents?: number;
  authorization_id?: string;
  capture_id?: string;
  status: "awaiting_buyer" | "authorized" | "captured" | "completed" | "voided" | "failed" | "refunded";
  approve_url?: string;
  failure?: string;
  debug_id?: string;
  refund_id?: string;
  created_at: string;
  updated_at: string;
}

export interface TimelineEvent {
  id: number;
  permission_id: string;
  decision_id?: string;
  type: string;
  data: Record<string, unknown>;
  at: string;
}

export interface Trial {
  label: string;
  merchant: string;
  category: string;
  amount_cents: number;
  instruction_source: string;
  decision: Outcome;
  reasons: string[];
  explanation: string;
}

export interface Preview {
  summary: string[];
  valid: boolean;
  errors: string[];
  trials: Trial[];
}

export interface Draft {
  permission: PermissionInput;
  question?: string;
  assumptions: string[];
}

export interface ServerConfig {
  paypal_configured: boolean;
  paypal_mode: "" | "approve" | "vault";
  compiler_configured: boolean;
  merchants: { id: string; name: string; return_window_days: number }[];
  sandbox: boolean;
}

export interface Connection {
  status: "none" | "pending" | "connected" | "failed" | "unavailable";
  approve_url?: string;
  vault_hint?: string;
}
