import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
  type AdminTabKey,
} from "@/components/admin/AdminShell";
import AdminPagination from "@/components/admin/AdminPagination";
import AdminTorrentList from "@/components/admin/AdminTorrentList";
import { fetchAdminTorrents } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";
import { formatCount } from "@/lib/format";

const PAGE_SIZE = 20;

const STATUS_OPTIONS = [
  { value: "", label: "全部状态" },
  { value: "unreviewed", label: "未审核" },
  { value: "reviewed", label: "已审核" },
];

/**
 * Shared body of /admin/torrents (full management) and /admin/review
 * (the unreviewed queue: status locked). Both are the same list
 * underneath; the queue just presets the filter. Selection with the
 * floating bulk bar is available on both.
 */
export default async function AdminTorrentBrowser({
  tab,
  lockedStatus,
  searchParams,
}: {
  tab: AdminTabKey;
  lockedStatus?: string;
  searchParams: Promise<{ q?: string; status?: string; page?: string }>;
}) {
  const params = await searchParams;
  const q = (params.q ?? "").trim();
  const status = lockedStatus ?? (params.status ?? "");
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);

  const outcome = await adminPageFetch((token) =>
    fetchAdminTorrents(token, { q, status, page, pageSize: PAGE_SIZE })
  );
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const data = outcome.data;
  const basePath = lockedStatus ? "/admin/review" : "/admin/torrents";

  return (
    <AdminShell active={tab}>
      {/* The filter bar sticks to the top of the viewport while the
          list scrolls: on a 20-row page the operator can re-filter
          without scrolling back up first. Solid canvas background so
          rows pass underneath it cleanly. */}
      <form
        method="get"
        action={basePath}
        className="sticky top-0 z-20 -mt-3 flex flex-wrap items-center gap-2 bg-canvas pt-3 pb-3"
      >
        <input
          type="search"
          name="q"
          defaultValue={q}
          placeholder="按名称关键词搜索"
          aria-label="按名称关键词搜索"
          className="min-h-11 min-w-0 flex-1 rounded-lg border border-input bg-surface px-4 text-base text-ink placeholder:text-faint focus:border-accent focus:outline-none sm:flex-none sm:basis-72"
        />
        {!lockedStatus && (
          <select
            name="status"
            defaultValue={status}
            aria-label="审核状态"
            className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink focus:border-accent focus:outline-none"
          >
            {STATUS_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        )}
        <button
          type="submit"
          className="inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover"
        >
          筛选
        </button>
        <span className="text-sm text-mute">
          共 {formatCount(data.total)} 条
        </span>
      </form>

      <div className="mt-4">
        <AdminTorrentList items={data.results} selectable />
      </div>

      <AdminPagination
        basePath={basePath}
        params={{ q, ...(lockedStatus ? {} : { status }) }}
        page={data.page}
        total={data.total}
        pageSize={data.page_size}
      />
    </AdminShell>
  );
}
