"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { AuthConfig } from "@/lib/admin";

// Friendly text for the ?error= codes the OAuth callbacks redirect
// back with (see lib/oauth.ts).
const OAUTH_ERROR_TEXT: Record<string, string> = {
  not_allowed: "你的账号不在放行名单内，无法登录。请联系管理员。",
  oauth_failed: "第三方登录失败，请重试或换一种登录方式。",
  oauth_denied: "你取消了第三方授权。",
  not_configured: "该登录方式尚未配置。",
  limited: "登录尝试过于频繁，请一分钟后再试。",
  unreachable: "后端不可达，请确认服务已启动。",
};

const STATUS_TEXT: Record<number, string> = {
  429: "登录尝试过于频繁，请一分钟后再试。",
  502: "后端不可达，请确认 Go 服务已启动。",
  503: "后端未启用该登录方式。",
};

function GitHubMark() {
  return (
    <svg
      width="20"
      height="20"
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
      className="shrink-0"
    >
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  );
}

const inputClass =
  "min-h-12 w-full rounded-lg border border-input bg-surface px-4 text-base text-ink placeholder:text-faint focus:border-accent focus:outline-none";
const primaryButtonClass =
  "inline-flex min-h-12 w-full items-center justify-center rounded-lg bg-accent px-4 text-base font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-50";
const federatedButtonClass =
  "inline-flex min-h-12 w-full items-center justify-center gap-3 rounded-lg border border-input bg-surface px-4 text-base font-medium text-ink transition-colors hover:border-accent hover:text-accent-ink";

export default function LoginForm({
  auth,
  errorCode,
}: {
  auth: AuthConfig;
  errorCode?: string;
}) {
  const router = useRouter();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(
    errorCode ? (OAUTH_ERROR_TEXT[errorCode] ?? "登录失败，请重试。") : null
  );

  const federated = auth.github.enabled || auth.oidc.enabled;
  // When accounts exist the password form is the primary path and
  // the recovery token folds away; on a token-only deployment the
  // token form stays primary and unfolded.
  const tokenPrimary = auth.token && !auth.password && !federated;

  async function post(payload: Record<string, string>, invalidText: string) {
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/admin-session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (res.ok) {
        router.push("/admin");
        router.refresh();
        return;
      }
      setError(
        res.status === 401
          ? invalidText
          : (STATUS_TEXT[res.status] ?? `登录失败（${res.status}）`)
      );
    } catch {
      setError("网络错误，请稍后重试。");
    } finally {
      setBusy(false);
    }
  }

  function submitPassword(e: React.FormEvent) {
    e.preventDefault();
    if (!username.trim() || !password || busy) return;
    void post({ username: username.trim(), password }, "用户名或密码不正确。");
  }

  function submitToken(e: React.FormEvent) {
    e.preventDefault();
    if (!token.trim() || busy) return;
    void post({ token: token.trim() }, "令牌不正确，请重新输入。");
  }

  const tokenForm = (
    <form onSubmit={submitToken}>
      <label htmlFor="admin-token" className="text-sm text-ink-2">
        管理令牌（后端环境变量 ADMIN_TOKEN）
      </label>
      <input
        id="admin-token"
        type="password"
        autoComplete="current-password"
        value={token}
        onChange={(e) => setToken(e.target.value)}
        placeholder="输入 ADMIN_TOKEN"
        className={`${inputClass} mt-2`}
      />
      <button
        type="submit"
        disabled={busy || !token.trim()}
        className={`${primaryButtonClass} mt-4`}
      >
        {busy ? "登录中…" : "用令牌登录"}
      </button>
    </form>
  );

  return (
    <div className="mt-6">
      {error && (
        <p
          role="alert"
          className="mb-4 rounded-lg border border-line bg-soft px-4 py-3 text-sm text-warn-ink"
        >
          {error}
        </p>
      )}

      {auth.password && (
        <form onSubmit={submitPassword}>
          <label htmlFor="admin-username" className="text-sm text-ink-2">
            用户名
          </label>
          <input
            id="admin-username"
            type="text"
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="管理员用户名"
            className={`${inputClass} mt-2`}
          />
          <label
            htmlFor="admin-password"
            className="mt-4 block text-sm text-ink-2"
          >
            密码
          </label>
          <input
            id="admin-password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="输入密码"
            className={`${inputClass} mt-2`}
          />
          <button
            type="submit"
            disabled={busy || !username.trim() || !password}
            className={`${primaryButtonClass} mt-4`}
          >
            {busy ? "登录中…" : "登录"}
          </button>
        </form>
      )}

      {federated && (
        <div className={auth.password ? "mt-6" : ""}>
          {auth.password && (
            <div className="mb-4 flex items-center gap-3 text-xs text-faint">
              <span className="h-px flex-1 bg-line" />
              或使用第三方账号
              <span className="h-px flex-1 bg-line" />
            </div>
          )}
          <div className="flex flex-col gap-3">
            {auth.github.enabled && (
              <a
                href="/api/auth/github?next=/admin"
                className={federatedButtonClass}
              >
                <GitHubMark />
                使用 GitHub 登录
              </a>
            )}
            {auth.oidc.enabled && (
              <a
                href="/api/auth/oidc?next=/admin"
                className={federatedButtonClass}
              >
                使用 {auth.oidc.name} 登录
              </a>
            )}
          </div>
        </div>
      )}

      {auth.token && tokenPrimary && <div>{tokenForm}</div>}
      {auth.token && !tokenPrimary && (
        <details className="mt-6 border-t border-line pt-4">
          <summary className="cursor-pointer text-sm text-mute hover:text-accent-ink">
            使用恢复令牌登录
          </summary>
          <div className="mt-3">{tokenForm}</div>
        </details>
      )}

      {!auth.has_users && auth.token && (
        <p className="mt-6 rounded-lg bg-soft px-4 py-3 text-xs leading-relaxed text-mute">
          还没有管理员账号：先用恢复令牌登录，然后在「账号」页创建第一个管理员，之后即可用用户名密码登录。
        </p>
      )}

      {!auth.token && !auth.password && !federated && (
        <p className="text-sm leading-relaxed text-mute">
          后端还没有配置任何登录方式。请在后端设置 ADMIN_TOKEN
          后重启服务，再回来用恢复令牌登录并创建管理员账号。
        </p>
      )}

      <p className="mt-6 text-xs leading-relaxed text-faint">
        登录成功后浏览器只保存一个 httpOnly 会话 cookie（24
        小时），密码与令牌不会留在浏览器里。
      </p>
    </div>
  );
}
