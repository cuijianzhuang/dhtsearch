// Proxy for visitor reports (POST /api/report on the Go backend).
//
// Server-side like every other backend call; the visitor's address is
// forwarded so the backend's per-client+torrent report limiter keys on
// the real reporter instead of this server. The backend's status and
// body are returned verbatim so the form can tell 201 (recorded),
// 429 (too many reports) and 400 (bad input) apart.

import { headers } from "next/headers";
import { NextResponse } from "next/server";
import { API_BASE } from "@/lib/api";

export async function POST(request: Request) {
  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return NextResponse.json({ error: "bad request" }, { status: 400 });
  }
  const h = await headers();
  const fwd: Record<string, string> = { "Content-Type": "application/json" };
  const xff = h.get("x-forwarded-for");
  if (xff) fwd["x-forwarded-for"] = xff;
  const realIP = h.get("x-real-ip");
  if (realIP) fwd["x-real-ip"] = realIP;

  let res: Response;
  try {
    res = await fetch(`${API_BASE}/api/report`, {
      method: "POST",
      cache: "no-store",
      headers: fwd,
      body: JSON.stringify(body),
    });
  } catch {
    return NextResponse.json({ error: "backend unreachable" }, { status: 502 });
  }
  const text = await res.text();
  return new NextResponse(text, {
    status: res.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
