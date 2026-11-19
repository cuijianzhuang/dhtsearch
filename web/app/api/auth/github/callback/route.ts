// GitHub login, callback: verify the state cookie, then hand the
// code to the Go backend (which holds the client secret) to finish.

import { cookies } from "next/headers";
import {
  GITHUB_STATE_COOKIE,
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
  const expected = store.get(GITHUB_STATE_COOKIE)?.value ?? "";
  if (!code || !state || !expected || state !== expected) {
    return loginErrorRedirect(request, "oauth_failed");
  }
  return completeOAuthLogin(request, "github", {
    code,
    redirect_uri: `${requestOrigin(request)}/api/auth/github/callback`,
  });
}
