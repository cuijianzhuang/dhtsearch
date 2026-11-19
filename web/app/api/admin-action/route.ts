// Action proxy for the admin console's client components.
//
// Browser code cannot call the Go admin API directly: the token lives
// in an httpOnly cookie it cannot read. Client components POST an
// action description here; this handler reads the cookie and forwards
// the corresponding call to the Go backend with the Bearer header,
// returning the backend's status and body verbatim.

import { NextResponse } from "next/server";
import { API_BASE } from "@/lib/api";
import { getAdminToken } from "@/lib/admin";

interface ActionBody {
  kind?: string;
  hash?: string;
  hashes?: string[];
  action?: string;
  reason?: string;
  /** kind="save-settings": settings key -> new value (null deletes
   *  the DB override, reverting to env/default). */
  values?: Record<string, string | null>;
}

export async function POST(request: Request) {
  const token = await getAdminToken();
  if (!token) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  let body: ActionBody;
  try {
    body = (await request.json()) as ActionBody;
  } catch {
    return NextResponse.json({ error: "bad request" }, { status: 400 });
  }

  let method = "POST";
  let path: string | null = null;
  let payload: unknown;
  const hash = encodeURIComponent(body.hash ?? "");
  switch (body.kind) {
    case "approve":
      path = `/api/admin/torrents/${hash}/approve`;
      break;
    case "block":
      path = `/api/admin/torrents/${hash}/block`;
      payload = { reason: body.reason ?? "admin" };
      break;
    case "delete":
      method = "DELETE";
      path = `/api/admin/torrents/${hash}`;
      break;
    case "unblock":
      method = "DELETE";
      path = `/api/admin/blocked/${hash}`;
      break;
    case "batch":
      path = `/api/admin/torrents/batch`;
      payload = {
        hashes: body.hashes ?? [],
        action: body.action ?? "",
        reason: body.reason ?? "admin",
      };
      break;
    case "save-settings":
      method = "PUT";
      path = `/api/admin/settings`;
      payload = { values: body.values ?? {} };
      break;
    default:
      return NextResponse.json({ error: "unknown action" }, { status: 400 });
  }
  if (body.kind !== "batch" && body.kind !== "save-settings" && !body.hash) {
    return NextResponse.json({ error: "hash required" }, { status: 400 });
  }

  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method,
      cache: "no-store",
      headers: {
        Authorization: `Bearer ${token}`,
        ...(payload ? { "Content-Type": "application/json" } : {}),
      },
      body: payload ? JSON.stringify(payload) : undefined,
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
