import type { Metadata } from "next";
import Link from "next/link";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import AdminPagination from "@/components/admin/AdminPagination";
import ReportsList from "@/components/admin/ReportsList";
import { fetchAdminReports } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";
import { formatCount } from "@/lib/format";

export const metadata: Metadata = { title: "举报队列" };

const PAGE_SIZE = 20;

const FILTERS = [
  { key: "pending", label: "待处理" },
  { key: "resolved", label: "已处理" },
  { key: "dismissed", label: "已忽略" },
  { key: "all", label: "全部" },
] as const;

export default async function AdminReportsPage({
  searchParams,
}: {
  searchParams: Promise<{ page?: string; status?: string }>;
}) {
  const params = await searchParams;
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);
  const status = FILTERS.some((f) => f.key === params.status)
    ? (params.status as string)
    : "pending";

  const outcome = await adminPageFetch((token) =>
    fetchAdminReports(token, { status, page, pageSize: PAGE_SIZE })
  );
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const data = outcome.data;

  return (
    <AdminShell active="reports">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-sm text-mute">
          共 {formatCount(data.total)} 条。访客从搜索结果详情里举报，
          拉黑会删除种子并阻止再次收录。
        </p>
        <nav aria-label="按状态筛选" className="ml-auto flex flex-wrap gap-2">
          {FILTERS.map((f) => (
            <Link
              key={f.key}
              href={`/admin/reports?status=${f.key}`}
              prefetch={false}
              className={`inline-flex min-h-11 items-center rounded-lg border px-3 text-sm transition-colors ${
                status === f.key
                  ? "border-accent bg-accent-soft font-medium text-accent-ink"
                  : "border-input bg-soft text-ink-2 hover:border-accent hover:text-accent-ink"
              }`}
            >
              {f.label}
            </Link>
          ))}
        </nav>
      </div>
      <div className="mt-4">
        <ReportsList items={data.results} />
      </div>
      <AdminPagination
        basePath="/admin/reports"
        params={{ status }}
        page={data.page}
        total={data.total}
        pageSize={data.page_size}
      />
    </AdminShell>
  );
}
