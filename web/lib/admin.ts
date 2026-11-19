// Admin console data layer (server-side only).
//
// Auth model: the operator's ADMIN_TOKEN lives in an httpOnly cookie
// (set by /api/admin-session after the Go backend accepts the token at
// /api/admin/login). Server Components read the cookie and call the Go
// admin API with it as a Bearer header; browser JavaScript never sees
// the token, so an XSS anywhere on the site cannot exfiltrate it —
// unlike sessionStorage, which any script on the page can read.

import { cookies, headers } from "next/headers";
import { API_BASE } from "./api";

export const ADMIN_COOKIE = "dhtsearch_admin";

export class AdminError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

/** The admin API is not configured on the backend (ADMIN_TOKEN unset). */
export function isAdminDisabled(e: unknown): boolean {
  return e instanceof AdminError && e.status === 503;
}

/** The stored token was rejected — the operator must log in again. */
export function isAdminUnauthorized(e: unknown): boolean {
  return e instanceof AdminError && e.status === 401;
}

export async function getAdminToken(): Promise<string | null> {
  const store = await cookies();
  return store.get(ADMIN_COOKIE)?.value ?? null;
}

// Forward the caller's identity like lib/api.ts does, so the backend's
// per-client rate limiting (especially the strict login limiter) sees
// the real visitor instead of the Next server.
async function clientIPHeaders(): Promise<Record<string, string>> {
  try {
    const h = await headers();
    const out: Record<string, string> = {};
    const xff = h.get("x-forwarded-for");
    if (xff) out["x-forwarded-for"] = xff;
    const realIP = h.get("x-real-ip");
    if (realIP) out["x-real-ip"] = realIP;
    return out;
  } catch {
    return {};
  }
}

export async function adminFetch<T>(
  token: string,
  path: string,
  init?: RequestInit
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      cache: "no-store",
      ...init,
      headers: {
        ...(await clientIPHeaders()),
        Authorization: `Bearer ${token}`,
        ...(init?.headers ?? {}),
      },
    });
  } catch {
    throw new AdminError(0, "backend unreachable");
  }
  if (!res.ok) {
    throw new AdminError(res.status, `admin API returned ${res.status}`);
  }
  return (await res.json()) as T;
}

export type LoginVerdict = "ok" | "invalid" | "disabled" | "limited" | "unreachable";

/** Ask the Go backend whether a token is the configured ADMIN_TOKEN. */
export async function verifyAdminToken(token: string): Promise<LoginVerdict> {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}/api/admin/login`, {
      method: "POST",
      cache: "no-store",
      headers: {
        "Content-Type": "application/json",
        ...(await clientIPHeaders()),
      },
      body: JSON.stringify({ token }),
    });
  } catch {
    return "unreachable";
  }
  switch (res.status) {
    case 200:
      return "ok";
    case 401:
      return "invalid";
    case 503:
      return "disabled";
    case 429:
      return "limited";
    default:
      return "unreachable";
  }
}

// --- Response types (mirror server/internal/api/admin.go) ---

export interface AdminCrawler {
  enabled: boolean;
  seen_infohashes: number;
  nodes: number;
  queued_nodes: number;
  sampled: number;
  sample_errors: number;
  harvested: number;
}

export interface AdminOverview {
  torrents: number;
  today: number;
  pending_review: number;
  reviewed: number;
  blocked: number;
  dialect: string;
  stats: Record<string, number>;
  fetch: { fetched: number; timed_out: number; failed: number };
  moderation: {
    reviewed: number;
    adult_removed: number;
    spam_removed: number;
    errors: number;
    pending: number;
    blocked: number;
  };
  crawler?: AdminCrawler;
  scraper?: {
    queue: number;
    scraped: number;
    seeded: number;
    scrape_errors: number;
    dropped: number;
    evicted: number;
  };
  feeds?: Record<string, number>;
  trending?: {
    movies: number;
    tv: number;
    tv_jp: number;
    tv_kr: number;
    updated_at: number;
  };
}

export interface AdminTorrentItem {
  info_hash: string;
  name: string;
  raw_name: string;
  alias?: string;
  total_size: number;
  file_count: number;
  created_at: number;
  reviewed_at: number;
  magnet: string;
}

export interface AdminListResponse {
  total: number;
  page: number;
  page_size: number;
  results: AdminTorrentItem[];
}

export interface AdminTorrentDetail extends AdminTorrentItem {
  clean_name?: string;
  files?: { path: string; size: number }[];
}

export interface BlockedItem {
  info_hash: string;
  reason: string;
  name: string;
  created_at: number;
}

export interface BlockedListResponse {
  total: number;
  page: number;
  page_size: number;
  results: BlockedItem[];
}

export function fetchAdminOverview(token: string): Promise<AdminOverview> {
  return adminFetch<AdminOverview>(token, "/api/admin/overview");
}

export function fetchAdminTorrents(
  token: string,
  opts: { q?: string; status?: string; page?: number; pageSize?: number }
): Promise<AdminListResponse> {
  const params = new URLSearchParams({
    q: opts.q ?? "",
    status: opts.status ?? "",
    page: String(opts.page ?? 1),
    page_size: String(opts.pageSize ?? 20),
  });
  return adminFetch<AdminListResponse>(token, `/api/admin/torrents?${params}`);
}

export function fetchAdminDetail(
  token: string,
  hash: string
): Promise<AdminTorrentDetail> {
  return adminFetch<AdminTorrentDetail>(
    token,
    `/api/admin/torrents/${encodeURIComponent(hash)}`
  );
}

export function fetchAdminBlocked(
  token: string,
  page = 1,
  pageSize = 20
): Promise<BlockedListResponse> {
  const params = new URLSearchParams({
    page: String(page),
    page_size: String(pageSize),
  });
  return adminFetch<BlockedListResponse>(token, `/api/admin/blocked?${params}`);
}

// --- Settings (mirror server/internal/api/settings.go) ---

export type AdminSettingType =
  | "bool"
  | "int"
  | "int64"
  | "float"
  | "duration"
  | "string"
  | "list"
  | "secret"
  | "enum";

export interface AdminSettingItem {
  key: string;
  label: string;
  desc?: string;
  type: AdminSettingType;
  /** Effective value in canonical string form; always "" for secrets. */
  value: string;
  /** Secrets only: whether a value is set at all. */
  set?: boolean;
  /** Which layer supplies the value: db (override) | env | default. */
  source: "db" | "env" | "default";
  editable: boolean;
  restart: boolean;
  sensitive: boolean;
  options?: string[];
}

export interface AdminSettingsGroup {
  key: string;
  label: string;
  items: AdminSettingItem[];
}

export interface AdminSettingsResponse {
  groups: AdminSettingsGroup[];
}

export function fetchAdminSettings(
  token: string
): Promise<AdminSettingsResponse> {
  return adminFetch<AdminSettingsResponse>(token, "/api/admin/settings");
}
