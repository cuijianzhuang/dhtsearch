// Proxy for the detail view's "similar torrents" lookup.
//
// Like every other backend call, this runs server-side: the browser
// asks this route, this route asks the Go API (which keeps its
// loopback-only deployment) and returns the JSON verbatim.

import { NextResponse } from "next/server";
import { fetchSimilar } from "@/lib/api";

export async function GET(request: Request) {
  const hash = new URL(request.url).searchParams.get("hash") ?? "";
  if (!/^[0-9a-fA-F]{40}$/.test(hash)) {
    return NextResponse.json({ error: "invalid hash" }, { status: 400 });
  }
  try {
    const results = await fetchSimilar(hash);
    return NextResponse.json({ results });
  } catch {
    return NextResponse.json({ error: "backend unreachable" }, { status: 502 });
  }
}
