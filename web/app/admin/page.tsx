import type { Metadata } from "next";
import Link from "next/link";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import OverviewDashboard from "@/components/admin/overview/OverviewDashboard";
import {
  fetchAdminOverview,
  fetchAdminTorrents,
  fetchAdminTrends,
  type AdminListResponse,
  type AdminTrends,
} from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";

export const metadata: Metadata = { title: "管理端概览" };

// Fallbacks for the two secondary fetches: the overview is the page's
// reason to exist, but a failed trends/latest call must not take the
// whole console down — the dashboard's poll retries them within
// seconds and fills the panels in.
const EMPTY_TRENDS: AdminTrends = { range: "24h", bucket_seconds: 3600, buckets: [] };
const EMPTY_LATEST: AdminListResponse = {
  total: 0,
  page: 1,
  page_size: 10,
  results: [],
};

export default async function AdminOverviewPage() {
  const outcome = await adminPageFetch(async (token) => {
    const overview = await fetchAdminOverview(token);
    const trends = await fetchAdminTrends(token, "24h").catch(
      () => EMPTY_TRENDS
    );
    const latest = await fetchAdminTorrents(token, {
      page: 1,
      pageSize: 10,
    }).catch(() => EMPTY_LATEST);
    return { overview, trends, latest };
  });
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const { overview, trends, latest } = outcome.data;
  return (
    <AdminShell active="overview">
      <OverviewDashboard
        initialOverview={overview}
        initialTrends={trends}
        initialLatest={latest}
      />
    </AdminShell>
  );
}
