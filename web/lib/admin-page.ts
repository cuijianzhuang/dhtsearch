// Shared guard plumbing for the admin pages (server-side).
//
// Every protected page follows the same flow: no cookie -> redirect to
// the login page; backend says the stored token is wrong (401) -> also
// redirect (the cookie is stale); backend has no ADMIN_TOKEN (503) ->
// the caller renders the "admin disabled" notice; anything else ->
// the caller renders the backend-error notice with the detail.

import { redirect } from "next/navigation";
import {
  AdminError,
  getAdminToken,
  isAdminDisabled,
  isAdminUnauthorized,
} from "./admin";

export async function tokenOrRedirect(): Promise<string> {
  const token = await getAdminToken();
  if (!token) redirect("/admin/login");
  return token;
}

export type AdminFetchOutcome<T> =
  | { ok: true; data: T }
  | { ok: false; kind: "disabled" | "error"; detail?: string };

export async function adminPageFetch<T>(
  fn: (token: string) => Promise<T>
): Promise<AdminFetchOutcome<T>> {
  const token = await tokenOrRedirect();
  try {
    return { ok: true, data: await fn(token) };
  } catch (e) {
    if (isAdminUnauthorized(e)) redirect("/admin/login");
    if (isAdminDisabled(e)) return { ok: false, kind: "disabled" };
    return {
      ok: false,
      kind: "error",
      detail: e instanceof AdminError ? e.message : String(e),
    };
  }
}
