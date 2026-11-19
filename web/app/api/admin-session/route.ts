// Login/logout for the admin console.
//
// POST accepts either {token} (the ADMIN_TOKEN recovery credential)
// or {username, password} (a local admin account). The credential is
// verified against the Go backend first; the backend trades it for a
// server-side session token, and only that session is stored, in an
// httpOnly cookie (see lib/admin.ts for why not sessionStorage).
// DELETE ends the backend session (best effort) and clears the
// cookie.

import { cookies } from "next/headers";
import { NextResponse } from "next/server";
import {
  ADMIN_COOKIE,
  clientIPHeaders,
  loginWithPassword,
  loginWithToken,
  type LoginOutcome,
} from "@/lib/admin";
import { API_BASE } from "@/lib/api";

const COOKIE_MAX_AGE = 24 * 60 * 60; // 24h, matching the backend session TTL

function verdictResponse(outcome: LoginOutcome) {
  switch (outcome.verdict) {
    case "invalid":
      return NextResponse.json({ error: "invalid credentials" }, { status: 401 });
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

export async function POST(request: Request) {
  let body: { token?: string; username?: string; password?: string };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return NextResponse.json({ error: "bad request" }, { status: 400 });
  }

  let outcome: LoginOutcome;
  let fallbackCredential: string;
  if (body.username || body.password) {
    const username = (body.username ?? "").trim();
    const password = body.password ?? "";
    if (!username || !password) {
      return NextResponse.json(
        { error: "username and password required" },
        { status: 400 }
      );
    }
    outcome = await loginWithPassword(username, password);
    fallbackCredential = "";
  } else {
    const token = (body.token ?? "").trim();
    if (!token) {
      return NextResponse.json({ error: "token required" }, { status: 400 });
    }
    outcome = await loginWithToken(token);
    fallbackCredential = token;
  }
  if (outcome.verdict !== "ok") {
    return verdictResponse(outcome);
  }

  const proto =
    request.headers.get("x-forwarded-proto") ??
    new URL(request.url).protocol.replace(":", "");
  const store = await cookies();
  store.set(ADMIN_COOKIE, outcome.session ?? fallbackCredential, {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    maxAge: COOKIE_MAX_AGE,
    secure: proto === "https",
  });
  return NextResponse.json({ ok: true, principal: outcome.principal });
}

export async function DELETE() {
  const store = await cookies();
  const credential = store.get(ADMIN_COOKIE)?.value;
  if (credential) {
    // Best effort: end the server-side session so the token stops
    // working everywhere, not just in this browser.
    try {
      await fetch(`${API_BASE}/api/admin/logout`, {
        method: "POST",
        cache: "no-store",
        headers: {
          Authorization: `Bearer ${credential}`,
          ...(await clientIPHeaders()),
        },
      });
    } catch {
      // The cookie is cleared either way.
    }
  }
  store.delete(ADMIN_COOKIE);
  return NextResponse.json({ ok: true });
}
