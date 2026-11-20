// Proxy for the detail view's paged file list.
//
// Like every other backend call, this runs server-side: the browser
// asks this route, this route asks the Go API (which keeps its
// loopback-only deployment) and returns the JSON verbatim.

import { NextResponse } from "next/server";
import { fetchTorrentFiles } from "@/lib/api";

export async function GET(request: Request) {
  const sp = new URL(request.url).searchParams;
  const hash = sp.get("hash") ?? "";
  if (!/^[0-9a-fA-F]{40}$/.test(hash)) {
    return NextResponse.json({ error: "invalid hash" }, { status: 400 });
  }
  const pageRaw = Number(sp.get("page") ?? "1");
  const page = Number.isFinite(pageRaw) && pageRaw >= 1 ? Math.floor(pageRaw) : 1;
  const sizeRaw = Number(sp.get("page_size") ?? "100");
  const pageSize =
    Number.isFinite(sizeRaw) && sizeRaw >= 1 ? Math.floor(sizeRaw) : 100;
  const q = (sp.get("q") ?? "").slice(0, 200);
  try {
    const data = await fetchTorrentFiles(hash, page, pageSize, q);
    if (!data) {
      return NextResponse.json({ error: "not found" }, { status: 404 });
    }
    return NextResponse.json(data);
  } catch {
    return NextResponse.json({ error: "backend unreachable" }, { status: 502 });
  }
}
