// API client for the DHTSearch Go backend.
//
// Every runtime caller of this module runs on the server: the pages are
// Server Components and the admin proxy is a route handler. Browser
// components import only the types exported below (`import type`, erased
// at build time), so no API request ever originates in the browser and
// nothing here ships to the client bundle.
//
// Base URL resolution, first hit wins:
//   API_BASE              plain runtime env, read by the Next server
//                         process at startup — NOT inlined at build time.
//                         install.sh sets this on the web service from
//                         --api-port, so the API port can change on an
//                         installed release package without a rebuild.
//   NEXT_PUBLIC_API_BASE  build-time env, inlined into the bundle by
//                         Next.js; the release workflow bakes
//                         http://127.0.0.1:8080 here.
//   default               http://localhost:8080 (local development).

import { headers } from "next/headers";

export const API_BASE =
  process.env.API_BASE ??
  process.env.NEXT_PUBLIC_API_BASE ??
  "http://localhost:8080";

// First-party credential for the backend's public-API gate. When an
// operator turns PUBLIC_API_REQUIRE_TOKEN on, external callers need a
// managed API token — and the site itself would be locked out too,
// because every fetch in this module is anonymous. install.sh
// generates one random INTERNAL_API_TOKEN into both services'
// environments; presented as X-Internal-Token it marks the request
// first-party (exempt from the token gate, never counted). This is
// safe to keep in this module: it only ever runs on the server (see
// the header comment), and a non-NEXT_PUBLIC_ name would not be
// inlined into a client bundle even if it were imported there.
const INTERNAL_API_TOKEN = process.env.INTERNAL_API_TOKEN ?? "";

export function internalTokenHeaders(): Record<string, string> {
  return INTERNAL_API_TOKEN
    ? { "X-Internal-Token": INTERNAL_API_TOKEN }
    : {};
}

// Forward the caller's identity on server-side API requests. The Go backend
// rate-limits per client IP; without these headers every SSR fetch would
// arrive from the Next server's own address, so one abusive visitor could
// exhaust the shared bucket for everyone.
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
    // Outside a request scope (e.g. build time) there is nothing to forward.
    return {};
  }
}

// Headers for a first-party API call: the visitor's IP identity plus
// the internal token.
async function firstPartyHeaders(): Promise<Record<string, string>> {
  return { ...(await clientIPHeaders()), ...internalTokenHeaders() };
}

export interface TorrentFile {
  path: string;
  size: number;
}

export interface SearchResult {
  info_hash: string;
  name: string;
  /** Publisher's title from an RSS source (dmhy/Nyaa), e.g. carrying the
   *  Chinese name of a release whose torrent name is romaji only. */
  alias?: string;
  total_size: number;
  file_count: number;
  files?: TorrentFile[];
  magnet: string;
  created_at: number;
  /** Category key computed by the backend from the main file's
   *  extension: video/audio/image/doc/software/archive/other. */
  category: string;
}

// Category/sort constants live in lib/search-ui.ts (client-safe) and
// are re-exported here for server-side callers.
export { CATEGORIES, SORTS, type SearchSort } from "./search-ui";
import type { SearchSort } from "./search-ui";

export interface SearchResponse {
  total: number;
  page: number;
  page_size: number;
  results: SearchResult[];
  /** The category filter and sort the backend applied (echoed). */
  category?: string;
  sort?: string;
  /** Per-category counts over the full keyword match set (before the
   *  category filter) — the tab badges. */
  categories?: Record<string, number>;
  /** Announce URLs the magnets were stamped with; shown in the
   *  detail view's tracker list. */
  trackers?: string[];
}

export interface StatsResponse {
  torrents?: number;
  seen?: number;
  fetched?: number;
  adult_filtered?: number;
  spam_filtered?: number;
  crawler_running?: boolean;
}

/** Why a search failed, in terms the search page can explain
 *  honestly instead of always claiming the backend is down:
 *  - "auth": the public API demands a token (401/403) — for the
 *    site's own page this means the first-party channel is not
 *    configured on both sides;
 *  - "rate": per-IP or per-token throttling (429);
 *  - "disabled": the operator turned the public API off (503 with
 *    the gate's own error body);
 *  - "unavailable": network failure, 5xx, or a busy-shedding 503. */
export type SearchErrorKind = "auth" | "rate" | "disabled" | "unavailable";

export class SearchError extends Error {
  kind: SearchErrorKind;
  status: number;
  constructor(kind: SearchErrorKind, status: number, message: string) {
    super(message);
    this.kind = kind;
    this.status = status;
  }
}

export async function fetchSearch(
  q: string,
  page: number,
  pageSize = 20,
  category = "",
  sort: SearchSort = "relevance"
): Promise<SearchResponse> {
  const params = new URLSearchParams({
    q,
    page: String(page),
    page_size: String(pageSize),
  });
  if (category) params.set("category", category);
  if (sort !== "relevance") params.set("sort", sort);
  let res: Response;
  try {
    res = await fetch(`${API_BASE}/api/search?${params.toString()}`, {
      cache: "no-store",
      headers: await firstPartyHeaders(),
    });
  } catch {
    throw new SearchError("unavailable", 0, "search API unreachable");
  }
  if (res.ok) {
    return (await res.json()) as SearchResponse;
  }
  // The gate and the load shedder both answer JSON {"error": ...};
  // read it to tell "public api disabled" apart from a busy 503.
  let backendError = "";
  try {
    const body = (await res.json()) as { error?: string };
    backendError = body.error ?? "";
  } catch {
    // Non-JSON error body — the status alone decides.
  }
  const detail = backendError || `search API returned ${res.status}`;
  if (res.status === 401 || res.status === 403) {
    throw new SearchError("auth", res.status, detail);
  }
  if (res.status === 429) {
    throw new SearchError("rate", res.status, "请求过于频繁，请稍后重试");
  }
  if (res.status === 503 && backendError.includes("public api disabled")) {
    throw new SearchError("disabled", res.status, detail);
  }
  throw new SearchError("unavailable", res.status, detail);
}

export interface TrendingResponse {
  movies?: string[];
  tv?: string[];
  tv_jp?: string[];
  tv_kr?: string[];
  updated_at?: number;
}

export async function fetchTrending(): Promise<TrendingResponse | null> {
  try {
    // The backend refreshes hourly; revalidate keeps the homepage from
    // hitting the API per request. No per-client IP headers here — they
    // would fragment this shared cache into one entry per visitor.
    const res = await fetch(`${API_BASE}/api/trending`, {
      next: { revalidate: 300 },
      // The internal token is a deployment constant, so it does not
      // fragment this shared cache the way per-visitor headers would.
      headers: internalTokenHeaders(),
    });
    if (!res.ok) return null;
    return (await res.json()) as TrendingResponse;
  } catch {
    // Trending is decorative — degrade to no section.
    return null;
  }
}

export interface SimilarItem {
  info_hash: string;
  name: string;
  total_size: number;
  file_count: number;
  magnet: string;
  created_at: number;
  category: string;
}

/** Similar torrents for the detail view; backs the /api/similar proxy
 *  route (the browser asks this server, never the Go API directly). */
export async function fetchSimilar(hash: string): Promise<SimilarItem[]> {
  const res = await fetch(
    `${API_BASE}/api/torrents/${encodeURIComponent(hash)}/similar`,
    { cache: "no-store", headers: await firstPartyHeaders() }
  );
  if (!res.ok) return [];
  const data = (await res.json()) as { results?: SimilarItem[] };
  return data.results ?? [];
}

/** One page of a torrent's stored file list. `total` counts the stored
 *  entries (what can actually be paged through), which can be lower
 *  than the torrent's file_count when the crawler capped the list. */
export interface TorrentFilesResponse {
  info_hash: string;
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
  files: TorrentFile[];
}

export async function fetchTorrentFiles(
  hash: string,
  page: number,
  pageSize = 100
): Promise<TorrentFilesResponse | null> {
  const params = new URLSearchParams({
    page: String(page),
    page_size: String(pageSize),
  });
  const res = await fetch(
    `${API_BASE}/api/torrents/${encodeURIComponent(hash)}/files?${params}`,
    { cache: "no-store", headers: await firstPartyHeaders() }
  );
  if (!res.ok) return null;
  return (await res.json()) as TorrentFilesResponse;
}

export async function fetchStats(): Promise<StatsResponse | null> {
  try {
    const res = await fetch(`${API_BASE}/api/stats`, {
      cache: "no-store",
      headers: await firstPartyHeaders(),
    });
    if (!res.ok) return null;
    return (await res.json()) as StatsResponse;
  } catch {
    // Backend unreachable — stats are optional, degrade gracefully.
    return null;
  }
}
