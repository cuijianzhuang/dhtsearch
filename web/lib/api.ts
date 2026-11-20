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
  const res = await fetch(`${API_BASE}/api/search?${params.toString()}`, {
    cache: "no-store",
    headers: await clientIPHeaders(),
  });
  if (res.status === 429) {
    throw new Error("请求过于频繁，请稍后重试");
  }
  if (res.status === 503) {
    throw new Error("服务器繁忙，请稍后重试");
  }
  if (!res.ok) {
    throw new Error(`search API returned ${res.status}`);
  }
  return (await res.json()) as SearchResponse;
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
    { cache: "no-store", headers: await clientIPHeaders() }
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
    { cache: "no-store", headers: await clientIPHeaders() }
  );
  if (!res.ok) return null;
  return (await res.json()) as TorrentFilesResponse;
}

export async function fetchStats(): Promise<StatsResponse | null> {
  try {
    const res = await fetch(`${API_BASE}/api/stats`, {
      cache: "no-store",
      headers: await clientIPHeaders(),
    });
    if (!res.ok) return null;
    return (await res.json()) as StatsResponse;
  } catch {
    // Backend unreachable — stats are optional, degrade gracefully.
    return null;
  }
}
