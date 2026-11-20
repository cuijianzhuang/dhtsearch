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
import { CATEGORIES } from "@/lib/search-ui";

const PAGE_SIZE = 20;

const STATUS_OPTIONS = [
  { value: "", label: "全部状态" },
  { value: "unreviewed", label: "未审核" },
  { value: "reviewed", label: "已审核" },
];

/** Sort orders of the admin list API (store.AdminSort*). The keys
 *  are the admin console's own set — wider than the public search's,
 *  because triage runs in both directions. */
const SORT_OPTIONS = [
  { value: "newest", label: "最新收录" },
  { value: "oldest", label: "最早收录" },
  { value: "size_desc", label: "体积最大" },
  { value: "size_asc", label: "体积最小" },
  { value: "files_desc", label: "文件最多" },
  { value: "seeders", label: "做种最多" },
];

/** Category filter options: the public search's taxonomy
 *  (lib/search-ui CATEGORIES, mirroring store.CategoryOf) plus the
 *  "all" entry. */
const CATEGORY_OPTIONS = [
  { value: "", label: "全部分类" },
  ...CATEGORIES.map((c) => ({ value: c.key, label: c.label })),
];

/** Size buckets of the admin list API (store adminSizeBuckets);
 *  boundaries are binary, as everywhere sizes display. */
const SIZE_OPTIONS = [
  { value: "", label: "全部大小" },
  { value: "lt10m", label: "< 10 MB" },
  { value: "10m_100m", label: "10–100 MB" },
  { value: "100m_1g", label: "100 MB – 1 GB" },
  { value: "1g_10g", label: "1–10 GB" },
  { value: "gt10g", label: "> 10 GB" },
];

/**
 * Shared body of /admin/torrents (full management) and /admin/review
 * (the unreviewed queue: status locked). Both are the same list
 * underneath; the queue just presets the filter. Selection with the
 * floating bulk bar is available on both. The sort/category/size
 * controls show on both pages — they stay meaningful in the queue
 * (biggest unreviewed first, only videos, …); only the status
 * select is locked away there, as before.
 */
export default async function AdminTorrentBrowser({
  tab,
  lockedStatus,
  searchParams,
}: {
  tab: AdminTabKey;
  lockedStatus?: string;
  searchParams: Promise<{
    q?: string;
    status?: string;
    sort?: string;
    category?: string;
    size?: string;
    page?: string;
  }>;
}) {
  const params = await searchParams;
  const q = (params.q ?? "").trim();
  const status = lockedStatus ?? (params.status ?? "");
  // An absent or unrecognized sort is the API default, newest.
  const sort = SORT_OPTIONS.some((o) => o.value === params.sort)
    ? (params.sort as string)
    : "newest";
  const category = params.category ?? "";
  const size = params.size ?? "";
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);

  const outcome = await adminPageFetch((token) =>
    fetchAdminTorrents(token, {
      q,
      status,
      sort,
      category,
      size,
      page,
      pageSize: PAGE_SIZE,
    })
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
          className="min-h-11 min-w-0 flex-1 max-sm:basis-full rounded-lg border border-input bg-surface px-4 text-base text-ink placeholder:text-faint focus:border-accent focus:outline-none sm:flex-none sm:basis-72"
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
        <select
          name="sort"
          defaultValue={sort}
          aria-label="排序"
          className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink focus:border-accent focus:outline-none"
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select
          name="category"
          defaultValue={category}
          aria-label="分类"
          className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink focus:border-accent focus:outline-none"
        >
          {CATEGORY_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select
          name="size"
          defaultValue={size}
          aria-label="大小"
          className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink focus:border-accent focus:outline-none"
        >
          {SIZE_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <button
          type="submit"
          className="inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover"
        >
          筛选
        </button>
        {/* The total anchors the row's right edge (ml-auto), so the
            toolbar reads controls-left / count-right instead of
            everything clustering at the left on wide screens. On
            mobile it wraps onto the select/button row, still right. */}
        <span className="ml-auto text-sm text-mute">
          共 {formatCount(data.total)} 条
        </span>
      </form>

      <div className="mt-4">
        <AdminTorrentList items={data.results} selectable />
      </div>

      <AdminPagination
        basePath={basePath}
        params={{
          q,
          ...(lockedStatus ? {} : { status }),
          sort,
          category,
          size,
        }}
        page={data.page}
        total={data.total}
        pageSize={data.page_size}
      />
    </AdminShell>
  );
}
