// Login/logout for the admin console.
//
// POST {token}: the token is verified against the Go backend's
// /api/admin/login first; only an accepted token is stored, in an
// httpOnly cookie (see lib/admin.ts for why not sessionStorage).
// DELETE: clears the cookie.

import { cookies } from "next/headers";
import { NextResponse } from "next/server";
import { ADMIN_COOKIE, verifyAdminToken } from "@/lib/admin";

const COOKIE_MAX_AGE = 24 * 60 * 60; // 24h

export async function POST(request: Request) {
  let token = "";
  try {
    const body = (await request.json()) as { token?: string };
    token = (body.token ?? "").trim();
  } catch {
    return NextResponse.json({ error: "bad request" }, { status: 400 });
  }
  if (!token) {
    return NextResponse.json({ error: "token required" }, { status: 400 });
  }
  const verdict = await verifyAdminToken(token);
  switch (verdict) {
    case "ok": {
      const proto =
        request.headers.get("x-forwarded-proto") ??
        new URL(request.url).protocol.replace(":", "");
      const store = await cookies();
      store.set(ADMIN_COOKIE, token, {
        httpOnly: true,
        sameSite: "lax",
        path: "/",
        maxAge: COOKIE_MAX_AGE,
        secure: proto === "https",
      });
      return NextResponse.json({ ok: true });
    }
    case "invalid":
      return NextResponse.json({ error: "invalid token" }, { status: 401 });
    case "disabled":
      return NextResponse.json({ error: "admin disabled" }, { status: 503 });
    case "limited":
      return NextResponse.json(
        { error: "too many login attempts" },
        { status: 429 }
      );
    default:
      return NextResponse.json(
        { error: "backend unreachable" },
        { status: 502 }
      );
  }
}

export async function DELETE() {
  const store = await cookies();
  store.delete(ADMIN_COOKIE);
  return NextResponse.json({ ok: true });
}
