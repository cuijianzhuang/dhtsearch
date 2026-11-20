import Link from "next/link";
import AdminShellClient, { type AdminTabKey } from "./AdminShellClient";
import { ADMIN_NAV_STORAGE_KEY } from "@/lib/theme";
import { fetchSiteConfig } from "@/lib/site";

export type { AdminTabKey };

// Applies the operator's stored sidebar choice (expanded/collapsed)
// before first paint. Same pattern as the appearance boot script in
// the root layout: a plain parser-blocking <script>, because an
// inline next/script would only run after the client runtime loads —
// the sidebar would paint expanded and then snap shut. The layout
// itself is CSS keyed off <html data-admin-nav> (globals.css, lg
// screens only); this script only restores the attribute.
const NAV_BOOT_SCRIPT = `try{var n=localStorage.getItem("${ADMIN_NAV_STORAGE_KEY}");if(n==="collapsed"||n==="expanded"){document.documentElement.dataset.adminNav=n;}}catch(e){}`;

/** Shared chrome for the protected admin pages: a left sidebar
 *  (collapsible on desktop, a drawer on mobile) rendered by
 *  AdminShellClient; this wrapper fetches the site config and emits
 *  the sidebar boot script. */
export default async function AdminShell({
  active,
  children,
}: {
  active: AdminTabKey;
  children: React.ReactNode;
}) {
  const site = await fetchSiteConfig();
  return (
    <>
      <script
        id="admin-nav-boot"
        dangerouslySetInnerHTML={{ __html: NAV_BOOT_SCRIPT }}
        suppressHydrationWarning
      />
      <AdminShellClient
        active={active}
        siteName={site.name}
        defaultAppearance={site.themeAppearance}
      >
        {children}
      </AdminShellClient>
    </>
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
