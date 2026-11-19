"use client";

// Account management for the admin console: who the signed-in
// principal is, a self-service password change for local accounts,
// and (admin role only) the account list with create / role /
// disable / reset-password / delete actions. Every mutation goes
// through the /api/admin-action proxy; the session cookie never
// reaches this code.

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { AdminMe, AdminUserItem } from "@/lib/admin";

const inputClass =
  "min-h-11 w-full rounded-lg border border-input bg-surface px-3 text-sm text-ink placeholder:text-faint focus:border-accent focus:outline-none";
const smallButtonClass =
  "inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-3 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50";
const primaryButtonClass =
  "inline-flex min-h-11 items-center justify-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-50";

const SOURCE_LABEL: Record<AdminUserItem["source"], string> = {
  local: "本地账号",
  github: "GitHub",
  oidc: "SSO",
};

const METHOD_LABEL: Record<string, string> = {
  token: "恢复令牌",
  password: "用户名密码",
  github: "GitHub",
  oidc: "SSO",
};

function formatTime(ts: number): string {
  if (!ts) return "从未登录";
  return new Date(ts * 1000).toLocaleString("zh-CN", { hour12: false });
}

async function postAction(
  body: Record<string, unknown>
): Promise<{ status: number; error?: string }> {
  const res = await fetch("/api/admin-action", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = (await res.json().catch(() => ({}))) as { error?: string };
  return { status: res.status, error: data.error };
}

export default function UsersManager({
  me,
  users,
}: {
  me: AdminMe;
  users: AdminUserItem[] | null;
}) {
  const router = useRouter();
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Create-account form.
  const [newName, setNewName] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [newRole, setNewRole] = useState<"admin" | "reviewer">("reviewer");

  // Self-service password change.
  const [currentPassword, setCurrentPassword] = useState("");
  const [nextPassword, setNextPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");

  // Per-row reset-password expansion.
  const [resetFor, setResetFor] = useState<string | null>(null);
  const [resetPassword, setResetPassword] = useState("");

  async function run(
    body: Record<string, unknown>,
    okText: string,
    after?: () => void
  ) {
    if (busy) return;
    setBusy(true);
    setNotice(null);
    try {
      const { status, error } = await postAction(body);
      if (status === 200) {
        setNotice(okText);
        after?.();
        router.refresh();
      } else {
        setNotice(`操作失败：${error ?? `HTTP ${status}`}`);
      }
    } catch {
      setNotice("操作失败：无法连接到后端。");
    } finally {
      setBusy(false);
    }
  }

  function createUser(e: React.FormEvent) {
    e.preventDefault();
    void run(
      {
        kind: "create-user",
        username: newName.trim(),
        password: newPassword,
        role: newRole,
      },
      `账号 ${newName.trim()} 已创建。`,
      () => {
        setNewName("");
        setNewPassword("");
        setNewRole("reviewer");
      }
    );
  }

  function changeMyPassword(e: React.FormEvent) {
    e.preventDefault();
    if (nextPassword !== confirmPassword) {
      setNotice("两次输入的新密码不一致。");
      return;
    }
    void run(
      {
        kind: "change-my-password",
        currentPassword,
        newPassword: nextPassword,
      },
      "密码已修改，其他设备上的会话已全部退出。",
      () => {
        setCurrentPassword("");
        setNextPassword("");
        setConfirmPassword("");
      }
    );
  }

  const isAdmin = me.role === "admin";
  const bootstrap = users !== null && users.length === 0;

  return (
    <div className="flex flex-col gap-6">
      <section className="rounded-xl border border-line bg-surface p-5 shadow-card">
        <h2 className="text-base font-semibold">当前登录</h2>
        <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
          <dt className="text-mute">身份</dt>
          <dd className="text-ink-2">
            {me.kind === "token"
              ? "恢复令牌（ADMIN_TOKEN）"
              : `${me.username}（${me.role === "admin" ? "管理员" : "审核员"}）`}
          </dd>
          <dt className="text-mute">登录方式</dt>
          <dd className="text-ink-2">{METHOD_LABEL[me.method] ?? me.method}</dd>
        </dl>
        {me.kind === "token" && (
          <p className="mt-3 text-xs leading-relaxed text-faint">
            恢复令牌拥有完整权限且无法单独吊销（改它要改后端环境变量并重启）。日常使用建议创建管理员账号后改用账号登录。
          </p>
        )}
      </section>

      {notice && (
        <p
          role="status"
          className="rounded-lg border border-line bg-soft px-4 py-3 text-sm text-ink-2"
        >
          {notice}
        </p>
      )}

      {me.kind === "user" && me.has_password && (
        <section className="rounded-xl border border-line bg-surface p-5 shadow-card">
          <h2 className="text-base font-semibold">修改我的密码</h2>
          <form
            onSubmit={changeMyPassword}
            className="mt-4 grid gap-3 sm:grid-cols-3"
          >
            <input
              type="password"
              autoComplete="current-password"
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
              placeholder="当前密码"
              aria-label="当前密码"
              className={inputClass}
            />
            <input
              type="password"
              autoComplete="new-password"
              value={nextPassword}
              onChange={(e) => setNextPassword(e.target.value)}
              placeholder="新密码（至少 8 位）"
              aria-label="新密码"
              className={inputClass}
            />
            <input
              type="password"
              autoComplete="new-password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              placeholder="再输一次新密码"
              aria-label="确认新密码"
              className={inputClass}
            />
            <div className="sm:col-span-3">
              <button
                type="submit"
                disabled={
                  busy || !currentPassword || nextPassword.length < 8 || !confirmPassword
                }
                className={primaryButtonClass}
              >
                修改密码
              </button>
              <span className="ml-3 text-xs text-faint">
                修改后其他设备上的会话会全部失效，本设备保持登录。
              </span>
            </div>
          </form>
        </section>
      )}

      {isAdmin && users !== null && (
        <>
          <section className="rounded-xl border border-line bg-surface p-5 shadow-card">
            <h2 className="text-base font-semibold">
              {bootstrap ? "创建第一个管理员账号" : "新建账号"}
            </h2>
            {bootstrap && (
              <p className="mt-2 text-sm leading-relaxed text-mute">
                目前还没有任何账号。创建第一个管理员后，登录页就会出现用户名密码登录。
              </p>
            )}
            <form
              onSubmit={createUser}
              className="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_auto_auto]"
            >
              <input
                type="text"
                autoComplete="off"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="用户名"
                aria-label="新账号用户名"
                className={inputClass}
              />
              <input
                type="password"
                autoComplete="new-password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                placeholder="初始密码（至少 8 位）"
                aria-label="新账号初始密码"
                className={inputClass}
              />
              <select
                value={newRole}
                onChange={(e) =>
                  setNewRole(e.target.value as "admin" | "reviewer")
                }
                aria-label="新账号角色"
                className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink focus:border-accent focus:outline-none"
              >
                <option value="admin">管理员</option>
                <option value="reviewer">审核员</option>
              </select>
              <button
                type="submit"
                disabled={busy || newName.trim().length < 3 || newPassword.length < 8}
                className={primaryButtonClass}
              >
                创建账号
              </button>
            </form>
            <p className="mt-3 text-xs leading-relaxed text-faint">
              管理员可改设置、管理账号；审核员只能看概览、处理种子与黑名单。GitHub
              / SSO 账号在对方首次登录时自动创建，不在这里建。
            </p>
          </section>

          <section className="rounded-xl border border-line bg-surface p-5 shadow-card">
            <h2 className="text-base font-semibold">
              全部账号（{users.length}）
            </h2>
            {users.length === 0 ? (
              <p className="mt-3 text-sm text-mute">暂无账号。</p>
            ) : (
              <ul className="mt-4 flex flex-col divide-y divide-line">
                {users.map((u) => (
                  <li key={u.id} className="py-4 first:pt-0 last:pb-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium text-ink">
                        {u.username}
                      </span>
                      <span className="rounded-md bg-accent-soft px-2 py-0.5 text-xs text-accent-ink">
                        {u.role === "admin" ? "管理员" : "审核员"}
                      </span>
                      <span className="rounded-md bg-soft px-2 py-0.5 text-xs text-mute">
                        {SOURCE_LABEL[u.source]}
                      </span>
                      {u.disabled && (
                        <span className="rounded-md bg-soft px-2 py-0.5 text-xs text-warn-ink">
                          已禁用
                        </span>
                      )}
                      <span className="ml-auto text-xs text-faint">
                        最后登录：{formatTime(u.last_login_at)}
                      </span>
                    </div>
                    <div className="mt-3 flex flex-wrap items-center gap-2">
                      <select
                        value={u.role}
                        disabled={busy}
                        aria-label={`${u.username} 的角色`}
                        onChange={(e) =>
                          void run(
                            {
                              kind: "update-user",
                              userId: u.id,
                              role: e.target.value,
                            },
                            `${u.username} 的角色已更新。`
                          )
                        }
                        className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink focus:border-accent focus:outline-none"
                      >
                        <option value="admin">管理员</option>
                        <option value="reviewer">审核员</option>
                      </select>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() =>
                          void run(
                            {
                              kind: "update-user",
                              userId: u.id,
                              disabled: !u.disabled,
                            },
                            u.disabled
                              ? `${u.username} 已启用。`
                              : `${u.username} 已禁用，其会话已全部退出。`
                          )
                        }
                        className={smallButtonClass}
                      >
                        {u.disabled ? "启用" : "禁用"}
                      </button>
                      {u.source === "local" && (
                        <button
                          type="button"
                          disabled={busy}
                          onClick={() => {
                            setResetFor(resetFor === u.id ? null : u.id);
                            setResetPassword("");
                          }}
                          className={smallButtonClass}
                        >
                          重置密码
                        </button>
                      )}
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => {
                          if (
                            window.confirm(
                              `确定删除账号 ${u.username}？其会话会立即失效。`
                            )
                          ) {
                            void run(
                              { kind: "delete-user", userId: u.id },
                              `${u.username} 已删除。`
                            );
                          }
                        }}
                        className={smallButtonClass}
                      >
                        删除
                      </button>
                    </div>
                    {resetFor === u.id && (
                      <form
                        className="mt-3 flex flex-wrap gap-2"
                        onSubmit={(e) => {
                          e.preventDefault();
                          void run(
                            {
                              kind: "reset-user-password",
                              userId: u.id,
                              password: resetPassword,
                            },
                            `${u.username} 的密码已重置，其会话已全部退出。`,
                            () => {
                              setResetFor(null);
                              setResetPassword("");
                            }
                          );
                        }}
                      >
                        <input
                          type="password"
                          autoComplete="new-password"
                          value={resetPassword}
                          onChange={(e) => setResetPassword(e.target.value)}
                          placeholder="新密码（至少 8 位）"
                          aria-label={`${u.username} 的新密码`}
                          className={`${inputClass} max-w-xs`}
                        />
                        <button
                          type="submit"
                          disabled={busy || resetPassword.length < 8}
                          className={primaryButtonClass}
                        >
                          确认重置
                        </button>
                      </form>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
        </>
      )}

      {!isAdmin && (
        <p className="text-sm text-mute">
          账号管理仅管理员可用；审核员只能处理审核相关页面。
        </p>
      )}
    </div>
  );
}
