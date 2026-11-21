import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import SettingsGroupForm from "@/components/admin/SettingsGroupForm";
import { SettingsNavRail } from "@/components/admin/SettingsNav";
import SettingsNavChips from "@/components/admin/SettingsNavChips";
import RestartCard from "@/components/admin/RestartCard";
import RetentionCard from "@/components/admin/RetentionCard";
import TrendingStatusCard from "@/components/admin/TrendingStatusCard";
import UpdateCard from "@/components/admin/UpdateCard";
import {
  fetchAdminOverview,
  fetchAdminRestart,
  fetchAdminRetention,
  fetchAdminSettings,
  fetchAdminUpdate,
} from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";
import {
  SETTINGS_GROUP_DESCRIPTIONS,
  SETTINGS_GROUP_LABELS,
} from "@/lib/settings-groups";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ group: string }>;
}): Promise<Metadata> {
  const { group } = await params;
  const label = SETTINGS_GROUP_LABELS[group];
  return { title: label ? `${label} · 设置` : "设置" };
}

// One settings group per page. The slug is the registry group key;
// the fetched snapshot is the source of truth for which groups
// exist — an unknown slug is a plain 404.
export default async function AdminSettingsGroupPage({
  params,
}: {
  params: Promise<{ group: string }>;
}) {
  const { group: slug } = await params;
  const outcome = await adminPageFetch((token) => fetchAdminSettings(token));
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const groups = outcome.data.groups;
  const group = groups.find((g) => g.key === slug);
  if (!group) notFound();

  // The update card belongs to the 系统更新 group; it degrades on
  // its own (it polls and retries), so a failed initial fetch just
  // means "no data yet", never a page error.
  const updateOutcome =
    slug === "update"
      ? await adminPageFetch((token) => fetchAdminUpdate(token))
      : null;

  // The restart card belongs to the 服务与数据库 group — the group
  // whose settings are mostly restart-required — and degrades the
  // same way.
  const restartOutcome =
    slug === "service"
      ? await adminPageFetch((token) => fetchAdminRestart(token))
      : null;

  // The retention card belongs to the 数据保留 group: it shows the
  // eligible-for-deletion count under the group's settings before
  // the operator enables them, and degrades the same way.
  const retentionOutcome =
    slug === "retention"
      ? await adminPageFetch((token) => fetchAdminRetention(token))
      : null;

  // The trending status card belongs to the 趋势 group: it shows the
  // fetcher's freshness (last change, last full success, failure
  // streak) from the overview payload, so a stuck fetcher is visible
  // where its settings live. Degrades the same way.
  const trendingOutcome =
    slug === "trending"
      ? await adminPageFetch((token) => fetchAdminOverview(token))
      : null;

  const navGroups = groups.map((g) => ({
    key: g.key,
    label: g.label,
    count: g.items.length,
  }));
  const description = SETTINGS_GROUP_DESCRIPTIONS[slug];

  return (
    <AdminShell active="settings">
      <div className="flex flex-col gap-6">
        <div>
          <Link
            href="/admin/settings"
            className="inline-flex min-h-11 items-center gap-1 text-sm text-mute transition-colors hover:text-accent-ink"
          >
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
              className="h-4 w-4"
              aria-hidden
            >
              <path d="m15 18-6-6 6-6" />
            </svg>
            设置总览
          </Link>
          <h1 className="mt-1 text-lg font-semibold text-ink">{group.label}</h1>
          {description && (
            <p className="mt-1 text-sm text-mute">{description}</p>
          )}
        </div>

        <div className="flex items-start gap-6">
          <SettingsNavRail groups={navGroups} active={slug} />
          <div className="flex min-w-0 flex-1 flex-col gap-6">
            <SettingsNavChips groups={navGroups} active={slug} />
            {updateOutcome && (
              <UpdateCard
                initial={updateOutcome.ok ? updateOutcome.data : null}
              />
            )}
            {restartOutcome && (
              <RestartCard
                initial={restartOutcome.ok ? restartOutcome.data : null}
              />
            )}
            {retentionOutcome && (
              <RetentionCard
                initial={retentionOutcome.ok ? retentionOutcome.data : null}
              />
            )}
            {trendingOutcome && (
              <TrendingStatusCard
                initial={
                  trendingOutcome.ok
                    ? (trendingOutcome.data.trending ?? null)
                    : null
                }
              />
            )}
            <SettingsGroupForm group={group} />
          </div>
        </div>
      </div>
    </AdminShell>
  );
}
