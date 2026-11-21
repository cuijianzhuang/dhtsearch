// Read proxy for the admin console's live views (the overview
// dashboard polls through here). Same trust model as
// /api/admin-action: the token lives in an httpOnly cookie the browser
// cannot read, so this handler reads the cookie and forwards a
// whitelisted GET to the Go backend, returning status and body
// verbatim. Only idempotent reads are exposed — mutations stay on the
// POST action proxy.

import { NextResponse } from "next/server";
import { API_BASE } from "@/lib/api";
import { getAdminToken } from "@/lib/admin";

const KINDS: Record<string, (q: URLSearchParams) => string> = {
  overview: () => "/api/admin/overview",
  trends: (q) =>
    `/api/admin/trends?range=${encodeURIComponent(q.get("range") ?? "24h")}`,
  latest: () => "/api/admin/torrents?page=1&page_size=10",
  update: () => "/api/admin/update",
  restart: () => "/api/admin/restart",
  retention: () => "/api/admin/retention",
};

export async function GET(request: Request) {
  const token = await getAdminToken();
  if (!token) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  const q = new URL(request.url).searchParams;
  const build = KINDS[q.get("kind") ?? ""];
  if (!build) {
    return NextResponse.json({ error: "unknown kind" }, { status: 400 });
  }
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${build(q)}`, {
      cache: "no-store",
      headers: { Authorization: `Bearer ${token}` },
    });
  } catch {
    return NextResponse.json(
      { error: "backend unreachable" },
      { status: 502 }
    );
  }
  const text = await res.text();
  return new NextResponse(text, {
    status: res.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
