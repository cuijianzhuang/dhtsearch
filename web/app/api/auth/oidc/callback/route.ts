// SSO (OIDC) login, callback: verify the state cookie, then hand
// code + nonce to the Go backend, which verifies the id_token.

import { cookies } from "next/headers";
import {
  OIDC_NONCE_COOKIE,
  OIDC_STATE_COOKIE,
  completeOAuthLogin,
  loginErrorRedirect,
  requestOrigin,
} from "@/lib/oauth";

export async function GET(request: Request) {
  const url = new URL(request.url);
  if (url.searchParams.get("error")) {
    return loginErrorRedirect(request, "oauth_denied");
  }
  const code = url.searchParams.get("code") ?? "";
  const state = url.searchParams.get("state") ?? "";
  const store = await cookies();
  const expectedState = store.get(OIDC_STATE_COOKIE)?.value ?? "";
  const nonce = store.get(OIDC_NONCE_COOKIE)?.value ?? "";
  if (!code || !state || !expectedState || state !== expectedState || !nonce) {
    return loginErrorRedirect(request, "oauth_failed");
  }
  return completeOAuthLogin(request, "oidc", {
    code,
    nonce,
    redirect_uri: `${requestOrigin(request)}/api/auth/oidc/callback`,
  });
}
