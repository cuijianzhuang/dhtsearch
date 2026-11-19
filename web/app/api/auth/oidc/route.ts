// SSO (OIDC) login, start: discover the provider's authorization
// endpoint, then bounce the browser there with state + nonce
// cookies tying the callback — and the id_token — to this browser.

import { NextResponse } from "next/server";
import { fetchAuthConfig } from "@/lib/admin";
import {
  NEXT_COOKIE,
  OIDC_NONCE_COOKIE,
  OIDC_STATE_COOKIE,
  flowCookieOptions,
  loginErrorRedirect,
  randomToken,
  requestOrigin,
  sanitizeNext,
} from "@/lib/oauth";

export async function GET(request: Request) {
  const cfg = await fetchAuthConfig();
  if (!cfg.oidc.enabled) {
    return loginErrorRedirect(request, "not_configured");
  }
  let authEndpoint = "";
  try {
    const issuer = cfg.oidc.issuer.replace(/\/$/, "");
    const res = await fetch(`${issuer}/.well-known/openid-configuration`, {
      cache: "no-store",
    });
    if (!res.ok) throw new Error(`discovery ${res.status}`);
    const doc = (await res.json()) as { authorization_endpoint?: string };
    authEndpoint = doc.authorization_endpoint ?? "";
  } catch {
    return loginErrorRedirect(request, "unreachable");
  }
  if (!authEndpoint) {
    return loginErrorRedirect(request, "unreachable");
  }
  const url = new URL(request.url);
  const state = randomToken();
  const nonce = randomToken();
  const authorize = new URL(authEndpoint);
  authorize.searchParams.set("client_id", cfg.oidc.client_id);
  authorize.searchParams.set(
    "redirect_uri",
    `${requestOrigin(request)}/api/auth/oidc/callback`
  );
  authorize.searchParams.set("response_type", "code");
  authorize.searchParams.set("scope", "openid email profile");
  authorize.searchParams.set("state", state);
  authorize.searchParams.set("nonce", nonce);
  const response = NextResponse.redirect(authorize);
  const opts = flowCookieOptions(request);
  response.cookies.set(OIDC_STATE_COOKIE, state, opts);
  response.cookies.set(OIDC_NONCE_COOKIE, nonce, opts);
  response.cookies.set(
    NEXT_COOKIE,
    sanitizeNext(url.searchParams.get("next")),
    opts
  );
  return response;
}
