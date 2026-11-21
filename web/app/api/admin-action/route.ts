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
  /** kind="report-*" / "*-api-token": the target row id. */
  id?: number;
  /** kind="save-settings": settings key -> new value (null deletes
   *  the DB override, reverting to env/default). */
  values?: Record<string, string | null>;
  /** kind="create-api-token": display name for the new token. */
  name?: string;
  /** kind="create-api-token" / "rate-api-token": per-minute quota
   *  (0 = unlimited; omitted on create = settings default). */
  ratePerMin?: number;
  /** kind="test-llm": probe overrides; empty fields mean "use the
   *  effective setting" on the backend. */
  base_url?: string;
  model?: string;
  api_key?: string;
  /** Account management kinds (see /admin/users). */
  userId?: string;
  username?: string;
  password?: string;
  role?: string;
  disabled?: boolean;
  currentPassword?: string;
  newPassword?: string;
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
    case "check-update":
      path = `/api/admin/update/check`;
      break;
    case "request-upgrade":
      path = `/api/admin/update/request`;
      break;
    case "restart-service":
      path = `/api/admin/restart`;
      break;
    case "run-retention":
      path = `/api/admin/retention/run`;
      break;
    case "create-api-token":
      path = `/api/admin/api-tokens`;
      payload = {
        name: body.name ?? "",
        ...(body.ratePerMin !== undefined
          ? { rate_per_min: body.ratePerMin }
          : {}),
      };
      break;
    case "enable-api-token":
      path = `/api/admin/api-tokens/${body.id}/enable`;
      break;
    case "disable-api-token":
      path = `/api/admin/api-tokens/${body.id}/disable`;
      break;
    case "delete-api-token":
      method = "DELETE";
      path = `/api/admin/api-tokens/${body.id}`;
      break;
    case "rate-api-token":
      method = "PUT";
      path = `/api/admin/api-tokens/${body.id}/rate`;
      payload = { rate_per_min: body.ratePerMin ?? 0 };
      break;
    case "test-llm":
      path = `/api/admin/moderation/test`;
      payload = {
        base_url: body.base_url ?? "",
        model: body.model ?? "",
        api_key: body.api_key ?? "",
      };
      break;
    case "report-resolve":
    case "report-dismiss":
    case "report-block": {
      if (!body.id || body.id <= 0) {
        return NextResponse.json({ error: "id required" }, { status: 400 });
      }
      const verb = body.kind.replace("report-", "");
      path = `/api/admin/reports/${body.id}/${verb}`;
      break;
    }
    case "create-user":
      path = `/api/admin/users`;
      payload = {
        username: body.username ?? "",
        password: body.password ?? "",
        role: body.role ?? "reviewer",
      };
      break;
    case "update-user": {
      path = `/api/admin/users/${encodeURIComponent(body.userId ?? "")}`;
      const patch: Record<string, unknown> = {};
      if (body.role !== undefined) patch.role = body.role;
      if (body.disabled !== undefined) patch.disabled = body.disabled;
      payload = patch;
      break;
    }
    case "reset-user-password":
      path = `/api/admin/users/${encodeURIComponent(body.userId ?? "")}/password`;
      payload = { password: body.password ?? "" };
      break;
    case "delete-user":
      method = "DELETE";
      path = `/api/admin/users/${encodeURIComponent(body.userId ?? "")}`;
      break;
    case "change-my-password":
      path = `/api/admin/me/password`;
      payload = {
        current_password: body.currentPassword ?? "",
        new_password: body.newPassword ?? "",
      };
      break;
    default:
      return NextResponse.json({ error: "unknown action" }, { status: 400 });
  }
  const tokenKinds = [
    "create-api-token",
    "enable-api-token",
    "disable-api-token",
    "delete-api-token",
    "rate-api-token",
  ];
  if (tokenKinds.includes(body.kind ?? "")) {
    if (body.kind !== "create-api-token" && (!body.id || body.id < 1)) {
      return NextResponse.json({ error: "id required" }, { status: 400 });
    }
  } else if (
    body.kind !== "batch" &&
    body.kind !== "save-settings" &&
    body.kind !== "check-update" &&
    body.kind !== "request-upgrade" &&
    body.kind !== "restart-service" &&
    body.kind !== "run-retention" &&
    body.kind !== "test-llm" &&
    body.kind !== "create-user" &&
    body.kind !== "update-user" &&
    body.kind !== "reset-user-password" &&
    body.kind !== "delete-user" &&
    body.kind !== "change-my-password" &&
    !body.kind?.startsWith("report-") &&
    !body.hash
  ) {
    return NextResponse.json({ error: "hash required" }, { status: 400 });
  }
  const needsUser =
    body.kind === "update-user" ||
    body.kind === "reset-user-password" ||
    body.kind === "delete-user";
  if (needsUser && !body.userId) {
    return NextResponse.json({ error: "userId required" }, { status: 400 });
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
