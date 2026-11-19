"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

const ERROR_TEXT: Record<number, string> = {
  401: "令牌不正确，请重新输入。",
  429: "登录尝试过于频繁，请一分钟后再试。",
  502: "后端不可达，请确认 Go 服务已启动。",
  503: "后端未启用管理端：请先在后端设置 ADMIN_TOKEN 并重启服务。",
};

export default function LoginForm() {
  const router = useRouter();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!token.trim() || busy) return;
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/admin-session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: token.trim() }),
      });
      if (res.ok) {
        router.push("/admin");
        router.refresh();
        return;
      }
      setError(ERROR_TEXT[res.status] ?? `登录失败（${res.status}）`);
    } catch {
      setError("网络错误，请稍后重试。");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="mt-6">
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
        className="mt-2 min-h-12 w-full rounded-lg border border-input bg-surface px-4 text-base text-ink placeholder:text-faint focus:border-accent focus:outline-none"
      />
      {error && (
        <p role="alert" className="mt-3 text-sm text-warn-ink">
          {error}
        </p>
      )}
      <button
        type="submit"
        disabled={busy || !token.trim()}
        className="mt-4 inline-flex min-h-12 w-full items-center justify-center rounded-lg bg-accent px-4 text-base font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-50"
      >
        {busy ? "登录中…" : "登录"}
      </button>
      <p className="mt-4 text-xs leading-relaxed text-faint">
        令牌经后端校验后只保存在 httpOnly cookie 中（24 小时），页面脚本无法读取。
      </p>
    </form>
  );
}
