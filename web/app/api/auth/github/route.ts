// GitHub login, start: bounce the browser to GitHub's authorize
// page with a state cookie tying the callback to this browser.

import { NextResponse } from "next/server";
import { fetchAuthConfig } from "@/lib/admin";
import {
  GITHUB_STATE_COOKIE,
  NEXT_COOKIE,
  flowCookieOptions,
  loginErrorRedirect,
  randomToken,
  requestOrigin,
  sanitizeNext,
} from "@/lib/oauth";

export async function GET(request: Request) {
  const cfg = await fetchAuthConfig();
  if (!cfg.github.enabled) {
    return loginErrorRedirect(request, "not_configured");
  }
  const url = new URL(request.url);
  const state = randomToken();
  const redirectURI = `${requestOrigin(request)}/api/auth/github/callback`;
  const authorize = new URL("https://github.com/login/oauth/authorize");
  authorize.searchParams.set("client_id", cfg.github.client_id);
  authorize.searchParams.set("redirect_uri", redirectURI);
  authorize.searchParams.set("state", state);
  authorize.searchParams.set("scope", "read:user");
  const response = NextResponse.redirect(authorize);
  const opts = flowCookieOptions(request);
  response.cookies.set(GITHUB_STATE_COOKIE, state, opts);
  response.cookies.set(
    NEXT_COOKIE,
    sanitizeNext(url.searchParams.get("next")),
    opts
  );
  return response;
}
