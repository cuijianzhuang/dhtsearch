// Browser-facing halves of the GitHub / OIDC login flows
// (server-side only).
//
// The split of responsibilities: these route handlers own what only
// the web app can do — redirects, the state/nonce cookies, and the
// admin session cookie — while the Go backend owns everything with
// a secret in it (code exchange, id_token verification, allowlists,
// account linking, session minting). A completed flow therefore
// ends with the same cookie /api/admin-session sets.

import { cookies } from "next/headers";
import { NextResponse } from "next/server";
import { ADMIN_COOKIE, clientIPHeaders } from "./admin";
import { API_BASE } from "./api";

export const NEXT_COOKIE = "auth_next";
export const GITHUB_STATE_COOKIE = "auth_state_github";
export const OIDC_STATE_COOKIE = "auth_state_oidc";
export const OIDC_NONCE_COOKIE = "auth_nonce_oidc";

const FLOW_COOKIE_MAX_AGE = 600; // 10 minutes to finish the round trip
export const ADMIN_COOKIE_MAX_AGE = 24 * 60 * 60; // matches the backend session TTL

/** The public origin of this web app, honouring the proxy headers
 *  the deployment's reverse proxy sets. OAuth redirect URIs are
 *  derived from it, so what the provider sees is exactly what the
 *  settings page tells the operator to register. */
export function requestOrigin(request: Request): string {
  const proto =
    request.headers.get("x-forwarded-proto") ??
    new URL(request.url).protocol.replace(":", "");
  const host =
    request.headers.get("x-forwarded-host") ??
    request.headers.get("host") ??
    new URL(request.url).host;
  return `${proto}://${host}`;
}

export function isSecure(request: Request): boolean {
  return requestOrigin(request).startsWith("https://");
}

/** Only same-site paths may be a post-login destination: anything
 *  else (absolute URLs, protocol-relative //host) falls back to the
 *  console home, so the login flow cannot be an open redirector. */
export function sanitizeNext(raw: string | null | undefined): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//")) return "/admin";
  return raw;
}

export function randomToken(): string {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function flowCookieOptions(request: Request) {
  return {
    httpOnly: true,
    sameSite: "lax" as const,
    path: "/api/auth",
    maxAge: FLOW_COOKIE_MAX_AGE,
    secure: isSecure(request),
  };
}

export function loginErrorRedirect(request: Request, code: string) {
  return NextResponse.redirect(
    new URL(`/admin/login?error=${encodeURIComponent(code)}`, requestOrigin(request))
  );
}

const STATUS_TO_ERROR: Record<number, string> = {
  401: "oauth_failed",
  403: "not_allowed",
  429: "limited",
  503: "not_configured",
  502: "unreachable",
};

/** POST a completed authorization code to the Go backend and, on
 *  success, set the admin cookie and redirect to the destination
 *  the start handler stashed. Any failure lands back on the login
 *  page with a friendly error code. */
export async function completeOAuthLogin(
  request: Request,
  provider: "github" | "oidc",
  payload: Record<string, string>
): Promise<NextResponse> {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}/api/admin/auth/${provider}/exchange`, {
      method: "POST",
      cache: "no-store",
      headers: {
        "Content-Type": "application/json",
        ...(await clientIPHeaders()),
      },
      body: JSON.stringify(payload),
    });
  } catch {
    return loginErrorRedirect(request, "unreachable");
  }
  if (!res.ok) {
    return loginErrorRedirect(request, STATUS_TO_ERROR[res.status] ?? "oauth_failed");
  }
  const data = (await res.json().catch(() => ({}))) as { session?: string };
  if (!data.session) {
    return loginErrorRedirect(request, "oauth_failed");
  }
  const store = await cookies();
  const next = sanitizeNext(store.get(NEXT_COOKIE)?.value);
  const response = NextResponse.redirect(new URL(next, requestOrigin(request)));
  response.cookies.set(ADMIN_COOKIE, data.session, {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    maxAge: ADMIN_COOKIE_MAX_AGE,
    secure: isSecure(request),
  });
  for (const name of [NEXT_COOKIE, GITHUB_STATE_COOKIE, OIDC_STATE_COOKIE, OIDC_NONCE_COOKIE]) {
    response.cookies.set(name, "", { path: "/api/auth", maxAge: 0 });
  }
  return response;
}
