import type { Metadata } from "next";
import Link from "next/link";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import SettingsHashRedirect from "@/components/admin/SettingsHashRedirect";
import { fetchAdminSettings } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";
import {
  SETTINGS_GROUP_DESCRIPTIONS,
  settingsGroupHref,
} from "@/lib/settings-groups";

export const metadata: Metadata = { title: "设置" };

// The settings index: one card per registry group, each linking to
// the group's own page. The page is short by construction — the
// groups' items live on the sub-pages, not here.
export default async function AdminSettingsPage() {
  const outcome = await adminPageFetch((token) => fetchAdminSettings(token));
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const groups = outcome.data.groups;
  return (
    <AdminShell active="settings">
      <SettingsHashRedirect keys={groups.map((g) => g.key)} />
      <div className="flex flex-col gap-6">
        <div>
          <h1 className="text-lg font-semibold text-ink">设置</h1>
          <p className="mt-1 text-sm text-mute">
            按分组拆成独立页面，点开一组修改。标「数据库」的项是管理端保存的覆盖值，优先于环境变量；改动按每项标注即时生效或需重启。启动配置只能查看，修改请编辑
            /etc/dhtsearch/dhtsearch.env 后重启服务。
          </p>
        </div>

        <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {groups.map((g) => {
            const editable = g.items.filter((it) => it.editable);
            const hasRestart = editable.some((it) => it.restart);
            const overridden = g.items.filter(
              (it) => it.source === "db"
            ).length;
            return (
              <li key={g.key}>
                <Link
                  href={settingsGroupHref(g.key)}
                  className="flex h-full flex-col gap-2 rounded-xl border border-line bg-surface p-4 shadow-card transition-colors hover:border-accent"
                >
                  <span className="flex items-center gap-2">
                    <span className="min-w-0 flex-1 truncate text-base font-medium text-ink">
                      {g.label}
                    </span>
                    <svg
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="2"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      className="h-4 w-4 shrink-0 text-faint"
                      aria-hidden
                    >
                      <path d="m9 18 6-6-6-6" />
                    </svg>
                  </span>
                  <span className="text-sm leading-relaxed text-mute">
                    {SETTINGS_GROUP_DESCRIPTIONS[g.key] ?? ""}
                  </span>
                  <span className="mt-auto flex flex-wrap items-center gap-2 pt-1 text-xs text-faint">
                    <span className="tabular-nums">{g.items.length} 项</span>
                    {editable.length === 0 && (
                      <span className="rounded-md bg-soft px-1.5 py-0.5 text-[11px] text-mute">
                        只读
                      </span>
                    )}
                    {hasRestart && (
                      <span className="rounded-md border border-line px-1.5 py-0.5 text-[11px] text-warn-ink">
                        含需重启项
                      </span>
                    )}
                    {overridden > 0 && (
                      <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-[11px] font-medium text-accent-ink">
                        已自定义 {overridden} 项
                      </span>
                    )}
                  </span>
                </Link>
              </li>
            );
          })}
        </ul>
      </div>
    </AdminShell>
  );
}
