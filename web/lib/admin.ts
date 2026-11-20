// Admin console data layer (server-side only).
//
// Auth model: a successful login (recovery token, username+password,
// GitHub OAuth or OIDC SSO) is traded at the Go backend for a
// server-side session token, and only that session token lives in
// the httpOnly cookie (set by /api/admin-session or the OAuth
// callback routes). Server Components read the cookie and call the
// Go admin API with it as a Bearer header; browser JavaScript never
// sees any credential, so an XSS anywhere on the site cannot
// exfiltrate one — unlike sessionStorage, which any script on the
// page can read.

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
export async function clientIPHeaders(): Promise<Record<string, string>> {
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

export interface AdminPrincipal {
  kind: "token" | "user";
  username: string;
  role: "admin" | "reviewer";
  method: string;
  has_password?: boolean;
}

export interface LoginOutcome {
  verdict: LoginVerdict;
  /** Session token to store in the cookie; absent on failure (and on
   *  a pre-session backend, where the caller falls back to the raw
   *  credential — the backend accepts both). */
  session?: string;
  principal?: AdminPrincipal;
}

async function postLogin(path: string, payload: unknown): Promise<LoginOutcome> {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method: "POST",
      cache: "no-store",
      headers: {
        "Content-Type": "application/json",
        ...(await clientIPHeaders()),
      },
      body: JSON.stringify(payload),
    });
  } catch {
    return { verdict: "unreachable" };
  }
  switch (res.status) {
    case 200: {
      const data = (await res.json().catch(() => ({}))) as {
        session?: string;
        principal?: AdminPrincipal;
      };
      return { verdict: "ok", session: data.session, principal: data.principal };
    }
    case 401:
      return { verdict: "invalid" };
    case 503:
      return { verdict: "disabled" };
    case 429:
      return { verdict: "limited" };
    default:
      return { verdict: "unreachable" };
  }
}

/** Trade the ADMIN_TOKEN recovery credential for a session. */
export function loginWithToken(token: string): Promise<LoginOutcome> {
  return postLogin("/api/admin/login", { token });
}

/** Trade a username + password pair for a session. */
export function loginWithPassword(
  username: string,
  password: string
): Promise<LoginOutcome> {
  return postLogin("/api/admin/auth/password", { username, password });
}

// --- Auth configuration (public; drives the login page) ---

export interface AuthConfig {
  has_users: boolean;
  token: boolean;
  password: boolean;
  github: { enabled: boolean; client_id: string };
  oidc: { enabled: boolean; issuer: string; client_id: string; name: string };
}

const AUTH_CONFIG_FALLBACK: AuthConfig = {
  has_users: false,
  token: false,
  password: false,
  github: { enabled: false, client_id: "" },
  oidc: { enabled: false, issuer: "", client_id: "", name: "SSO" },
};

/** Which login methods the backend offers. Public by design: client
 *  IDs are not secrets, and the page must render before login. */
export async function fetchAuthConfig(): Promise<AuthConfig> {
  try {
    const res = await fetch(`${API_BASE}/api/admin/auth/config`, {
      cache: "no-store",
    });
    if (!res.ok) return AUTH_CONFIG_FALLBACK;
    const data = (await res.json()) as Partial<AuthConfig>;
    return {
      ...AUTH_CONFIG_FALLBACK,
      ...data,
      github: { ...AUTH_CONFIG_FALLBACK.github, ...(data.github ?? {}) },
      oidc: { ...AUTH_CONFIG_FALLBACK.oidc, ...(data.oidc ?? {}) },
    };
  } catch {
    return AUTH_CONFIG_FALLBACK;
  }
}

// --- Principal and accounts ---

export interface AdminMe extends AdminPrincipal {
  has_users: boolean;
}

export function fetchMe(token: string): Promise<AdminMe> {
  return adminFetch<AdminMe>(token, "/api/admin/me");
}

export interface AdminUserItem {
  id: string;
  username: string;
  role: "admin" | "reviewer";
  source: "local" | "github" | "oidc";
  disabled: boolean;
  created_at: number;
  last_login_at: number;
}

export function fetchAdminUsers(
  token: string
): Promise<{ users: AdminUserItem[]; total: number }> {
  return adminFetch(token, "/api/admin/users");
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
  tracked_nodes: number;
  avg_node_score: number;
  banned_nodes: number;
  restored_nodes: number;
}

export interface AdminOverview {
  torrents: number;
  today: number;
  pending_review: number;
  reviewed: number;
  blocked: number;
  /** Visitor reports awaiting handling (front-features). */
  reports_pending?: number;
  dialect: string;
  stats: Record<string, number>;
  fetch: {
    fetched: number;
    timed_out: number;
    failed: number;
    prescreen_dropped: number;
    queue_depth?: number;
  };
  moderation: {
    /** Whether the LLM pass can run (switch on AND API key set). */
    enabled: boolean;
    reviewed: number;
    adult_removed: number;
    spam_removed: number;
    errors: number;
    pending: number;
    blocked: number;
  };
  filter?: {
    enabled: boolean;
    adult_enabled: boolean;
    spam_enabled: boolean;
    too_small_enabled: boolean;
    adult_filtered: number;
    spam_filtered: number;
    size_filtered: number;
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
    failing?: boolean;
    fail_streak?: number;
    last_success?: number;
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
  /** Tracker-scrape swarm snapshot, same shape as the public
   *  search results: counters null when never scraped. */
  seeders?: number | null;
  leechers?: number | null;
  completed?: number | null;
  scraped_at?: number | null;
  zero_since?: number | null;
  zero_count?: number | null;
  health?: string;
}

/** One page of a torrent's stored file list (admin detail view). */
export interface AdminFilesResponse {
  info_hash: string;
  /** Entries being paged through: the filtered count when q is set. */
  total: number;
  /** The torrent's full stored entry count, filter or not. */
  total_all: number;
  page: number;
  page_size: number;
  total_pages: number;
  files: { path: string; size: number }[];
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

export interface ReportItem {
  id: number;
  info_hash: string;
  reason: "adult" | "spam" | "infringement" | "other" | string;
  note: string;
  reporter_ip: string;
  status: number; // 0 pending, 1 resolved, 2 dismissed
  created_at: number;
  /** Current display name of the reported torrent; "" if deleted. */
  name: string;
}

export interface ReportsListResponse {
  total: number;
  page: number;
  page_size: number;
  results: ReportItem[];
}

export function fetchAdminReports(
  token: string,
  opts: { status?: string; page?: number; pageSize?: number }
): Promise<ReportsListResponse> {
  const params = new URLSearchParams({
    status: opts.status ?? "pending",
    page: String(opts.page ?? 1),
    page_size: String(opts.pageSize ?? 20),
  });
  return adminFetch<ReportsListResponse>(token, `/api/admin/reports?${params}`);
}

export function fetchAdminOverview(token: string): Promise<AdminOverview> {
  return adminFetch<AdminOverview>(token, "/api/admin/overview");
}

export type AdminTrendsRange = "24h" | "14d";

export interface AdminTrends {
  range: AdminTrendsRange;
  bucket_seconds: number;
  buckets: { start: number; count: number }[];
}

export function fetchAdminTrends(
  token: string,
  range: AdminTrendsRange
): Promise<AdminTrends> {
  return adminFetch<AdminTrends>(token, `/api/admin/trends?range=${range}`);
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

export function fetchAdminFiles(
  token: string,
  hash: string,
  page = 1,
  pageSize = 100,
  q = ""
): Promise<AdminFilesResponse> {
  const params = new URLSearchParams({
    page: String(page),
    page_size: String(pageSize),
  });
  if (q) params.set("q", q);
  return adminFetch<AdminFilesResponse>(
    token,
    `/api/admin/torrents/${encodeURIComponent(hash)}/files?${params}`
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

// --- Public API tokens (mirror server/internal/store/apitokens.go) ---

/**
 * One public-API credential as the console may see it. The plaintext
 * and its hash are never in here — the plaintext comes back exactly
 * once from the create call, and only `prefix` identifies the token
 * afterwards.
 */
export interface APITokenItem {
  id: number;
  name: string;
  prefix: string;
  enabled: boolean;
  /** Per-minute quota; 0 = unlimited. */
  rate_per_min: number;
  call_count: number;
  /** Unix seconds; 0 = never used. */
  last_used_at: number;
  created_at: number;
}

export interface APITokenListResponse {
  results: APITokenItem[];
}

export interface CreateAPITokenResponse {
  /** The plaintext token — shown once, never retrievable again. */
  token: string;
  item: APITokenItem;
}

export function fetchAdminAPITokens(
  token: string
): Promise<APITokenListResponse> {
  return adminFetch<APITokenListResponse>(token, "/api/admin/api-tokens");
}

// --- LLM moderation console (mirror server/internal/api/moderation.go) ---

export interface ModerationRecentItem {
  info_hash: string;
  name: string;
  /** Verdict: ok | adult | spam. */
  label: string;
  /** Outcome: kept | deleted | would_delete (dry-run). */
  action: string;
  created_at: number;
}

export interface AdminModeration {
  enabled: boolean;
  key_set: boolean;
  model: string;
  base_url: string;
  dry_run: boolean;
  pending: number;
  stats: {
    reviewed: number;
    adult_removed: number;
    spam_removed: number;
    errors: number;
    titles_trimmed: number;
  };
  recent: ModerationRecentItem[];
}

export function fetchAdminModeration(
  token: string
): Promise<AdminModeration> {
  return adminFetch<AdminModeration>(token, "/api/admin/moderation");
}
