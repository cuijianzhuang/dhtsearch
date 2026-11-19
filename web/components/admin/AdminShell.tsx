import Link from "next/link";
import LogoutButton from "./LogoutButton";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";
import { fetchSiteConfig } from "@/lib/site";

const TABS = [
  { key: "overview", href: "/admin", label: "概览" },
  { key: "torrents", href: "/admin/torrents", label: "种子管理" },
  { key: "review", href: "/admin/review", label: "未审核队列" },
  { key: "blocked", href: "/admin/blocked", label: "黑名单" },
  { key: "llm", href: "/admin/llm", label: "LLM 审核" },
  { key: "reports", href: "/admin/reports", label: "举报队列" },
  { key: "users", href: "/admin/users", label: "账号" },
  { key: "settings", href: "/admin/settings", label: "设置" },
  { key: "api-tokens", href: "/admin/api-tokens", label: "API 令牌" },
] as const;

export type AdminTabKey = (typeof TABS)[number]["key"];

/** Shared chrome for the protected admin pages: title, tab nav, logout. */
export default async function AdminShell({
  active,
  children,
}: {
  active: AdminTabKey;
  children: React.ReactNode;
}) {
  const site = await fetchSiteConfig();
  return (
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col px-4 py-6">
      <header className="flex flex-wrap items-center gap-3">
        <Link
          href="/admin"
          className="inline-flex min-h-11 items-center text-xl font-bold tracking-tight"
        >
          <SiteLogo name={site.name} />
          <span className="ml-3 rounded-md bg-accent-soft px-2 py-1 text-xs font-medium text-accent-ink">
            管理端
          </span>
        </Link>
        <div className="ml-auto flex items-center gap-2">
          <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
          <LogoutButton />
        </div>
      </header>
      <nav className="mt-4 flex flex-wrap gap-2" aria-label="管理端导航">
        {TABS.map((tab) => (
          <Link
            key={tab.key}
            href={tab.href}
            aria-current={active === tab.key ? "page" : undefined}
            className={`inline-flex min-h-11 items-center rounded-lg border px-4 text-sm transition-colors ${
              active === tab.key
                ? "border-accent bg-accent-soft font-medium text-accent-ink"
                : "border-input bg-soft text-ink-2 hover:border-accent hover:text-accent-ink"
            }`}
          >
            {tab.label}
          </Link>
        ))}
        <Link
          href="/"
          className="inline-flex min-h-11 items-center rounded-lg border border-line bg-surface px-4 text-sm text-mute transition-colors hover:text-accent-ink"
        >
          返回前台
        </Link>
      </nav>
      <div className="mt-6 flex flex-1 flex-col">{children}</div>
      <footer className="mt-auto pt-10 text-center text-xs text-faint">
        {site.name} 管理端 · 数据来自后端 /api/admin
      </footer>
    </main>
  );
}

/** Shown when the backend has no login method configured at all. */
export function AdminDisabledNotice() {
  return (
    <main className="mx-auto flex w-full max-w-xl flex-1 flex-col px-4 py-16">
      <div className="rounded-xl border border-line bg-surface p-6 text-center shadow-card">
        <p className="text-lg font-medium text-ink-2">管理端未启用</p>
        <p className="mt-3 text-sm leading-relaxed text-mute">
          后端没有配置任何登录方式，/api/admin 接口整体禁用。请在后端环境变量中设置{" "}
          <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">
            ADMIN_TOKEN
          </code>{" "}
          后重启服务，再回到这里用恢复令牌登录并创建管理员账号。
        </p>
        <Link
          href="/"
          className="mt-6 inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 hover:border-accent hover:text-accent-ink"
        >
          返回前台
        </Link>
      </div>
    </main>
  );
}

/** Shown when the backend cannot be reached at all. */
export function AdminBackendErrorNotice({ detail }: { detail?: string }) {
  return (
    <main className="mx-auto flex w-full max-w-xl flex-1 flex-col px-4 py-16">
      <div className="rounded-xl border border-line bg-surface p-6 text-center shadow-card">
        <p className="text-lg font-medium text-ink-2">无法连接到后端</p>
        <p className="mt-3 text-sm leading-relaxed text-mute">
          管理接口暂时不可达，请确认 Go 后端已在运行（默认{" "}
          <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">
            http://localhost:8080
          </code>
          ）。
        </p>
        {detail && <p className="mt-2 font-mono text-xs text-faint">{detail}</p>}
        <Link
          href="/admin"
          className="mt-6 inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 hover:border-accent hover:text-accent-ink"
        >
          重试
        </Link>
      </div>
    </main>
  );
}
