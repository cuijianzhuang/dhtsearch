import type { Metadata } from "next";
import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import AdminPagination from "@/components/admin/AdminPagination";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import { ReviewBadge } from "@/components/admin/AdminTorrentList";
import TorrentActions from "@/components/admin/TorrentActions";
import CopyMagnetButton from "@/components/CopyMagnetButton";
import {
  AdminError,
  fetchAdminDetail,
  fetchAdminFiles,
  isAdminDisabled,
  isAdminUnauthorized,
  type AdminFilesResponse,
} from "@/lib/admin";
import { tokenOrRedirect } from "@/lib/admin-page";
import {
  formatCount,
  formatDate,
  formatRelativeTime,
  formatSize,
  formatSpan,
} from "@/lib/format";

export const metadata: Metadata = { title: "种子详情" };

const FILES_PAGE_SIZE = 100;

export default async function AdminTorrentDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ hash: string }>;
  searchParams: Promise<{ page?: string; fq?: string }>;
}) {
  const { hash } = await params;
  const { page: pageRaw, fq: fqRaw } = await searchParams;
  const fileQuery = (fqRaw ?? "").trim();
  const parsedPage = Number.parseInt(pageRaw ?? "1", 10);
  const filePage = Number.isFinite(parsedPage) && parsedPage >= 1 ? parsedPage : 1;
  const token = await tokenOrRedirect();
  let t;
  try {
    t = await fetchAdminDetail(token, hash);
  } catch (e) {
    if (isAdminUnauthorized(e)) redirect("/admin/login");
    if (isAdminDisabled(e)) return <AdminDisabledNotice />;
    if (e instanceof AdminError && e.status === 404) notFound();
    return (
      <AdminBackendErrorNotice
        detail={e instanceof AdminError ? e.message : String(e)}
      />
    );
  }
  // The file list is paged separately: the detail payload no longer
  // embeds it, so a torrent with thousands of files renders one page
  // (100 rows) instead of the whole list. A files-fetch failure only
  // degrades this section, not the whole page.
  let filesResp: AdminFilesResponse | null = null;
  let filesError: string | null = null;
  try {
    filesResp = await fetchAdminFiles(token, hash, filePage, FILES_PAGE_SIZE, fileQuery);
  } catch (e) {
    if (isAdminUnauthorized(e)) redirect("/admin/login");
    if (isAdminDisabled(e)) return <AdminDisabledNotice />;
    filesError = e instanceof AdminError ? e.message : String(e);
  }
  const files = filesResp?.files ?? [];
  const filesTotal = filesResp?.total ?? 0;
  const filesTotalAll = filesResp?.total_all ?? filesTotal;

  return (
    <AdminShell active="torrents">
      <Link
        href="/admin/torrents"
        className="inline-flex min-h-11 items-center text-sm text-mute hover:text-accent-ink"
      >
        ← 返回种子管理
      </Link>

      <div className="mt-2 rounded-xl border border-line bg-surface p-4 shadow-card sm:p-6">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 flex-1 text-lg font-semibold wrap-anywhere text-ink">
            {t.name || "(未命名)"}
          </h1>
          <ReviewBadge reviewed={t.reviewed_at > 0} />
        </div>
        {t.raw_name && t.raw_name !== t.name && (
          <p className="mt-2 text-sm wrap-anywhere text-mute">
            原始名称：{t.raw_name}
          </p>
        )}
        {t.alias && (
          <p className="mt-1 text-sm wrap-anywhere text-mute">
            发布标题：{t.alias}
          </p>
        )}

        <dl className="mt-4 grid grid-cols-2 gap-3 text-sm md:grid-cols-4">
          <div>
            <dt className="text-xs text-mute">总大小</dt>
            <dd className="mt-1 font-medium text-ink-2">{formatSize(t.total_size)}</dd>
          </div>
          <div>
            <dt className="text-xs text-mute">文件数</dt>
            <dd className="mt-1 font-medium text-ink-2">{formatCount(t.file_count)}</dd>
          </div>
          <div>
            <dt className="text-xs text-mute">收录时间</dt>
            <dd className="mt-1 font-medium text-ink-2">
              {formatDate(t.created_at)}（{formatRelativeTime(t.created_at)}）
            </dd>
          </div>
          <div>
            <dt className="text-xs text-mute">审核时间</dt>
            <dd className="mt-1 font-medium text-ink-2">
              {t.reviewed_at > 0 ? formatDate(t.reviewed_at) : "未审核"}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-mute">做种 / 下载 / 完成</dt>
            <dd className="mt-1 font-medium text-ink-2">
              {t.scraped_at != null
                ? `${formatCount(t.seeders)} / ${formatCount(t.leechers)} / ${formatCount(t.completed)}`
                : "暂无抓取数据"}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-mute">健康度</dt>
            <dd className="mt-1 font-medium text-ink-2">
              {t.health === "likely_dead" ? (
                <span className="inline-flex items-center rounded-md bg-soft px-2 py-1 text-xs font-medium text-warn-ink">
                  疑似死种
                </span>
              ) : t.health === "alive" ? (
                "有做种"
              ) : t.health === "no_seed" ? (
                "无完整源（有人在下）"
              ) : t.health === "suspect" ? (
                "无做种（观察中）"
              ) : (
                "未知"
              )}
              {t.scraped_at != null && (
                <span className="ml-2 text-xs font-normal text-mute">
                  更新于 {formatRelativeTime(t.scraped_at)}
                  {t.health === "likely_dead" &&
                    t.zero_since != null &&
                    ` · 连续 ${formatCount(t.zero_count)} 次零做种，已持续 ${formatSpan(t.scraped_at - t.zero_since)}`}
                </span>
              )}
            </dd>
          </div>
        </dl>

        <p className="mt-4 font-mono text-xs break-all text-faint">{t.info_hash}</p>

        <div className="mt-4 flex flex-wrap items-center gap-2">
          <TorrentActions
            hash={t.info_hash}
            name={t.name || t.info_hash}
            reviewed={t.reviewed_at > 0}
            afterDeleteHref="/admin/torrents"
          />
          <span className="inline-flex">
            <CopyMagnetButton magnet={t.magnet} />
          </span>
        </div>

        <h2 className="mt-6 text-sm font-semibold text-ink">
          文件列表（共 {formatCount(filesTotalAll)} 个
          {fileQuery && ` · 筛出 ${formatCount(filesTotal)} 个`}）
        </h2>
        <form
          method="get"
          action={`/admin/torrents/${t.info_hash}`}
          className="mt-2 flex flex-wrap items-center gap-2"
        >
          <input
            type="search"
            name="fq"
            defaultValue={fileQuery}
            placeholder="按文件名过滤…"
            aria-label="按文件名过滤文件列表"
            className="min-h-11 min-w-0 flex-1 rounded-lg border border-input bg-surface px-3 text-sm text-ink placeholder:text-faint focus:border-accent focus:outline-none sm:flex-none sm:basis-72"
          />
          <button
            type="submit"
            className="inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover"
          >
            过滤
          </button>
          {fileQuery && (
            <Link
              href={`/admin/torrents/${t.info_hash}`}
              className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-3 text-sm text-ink-2 hover:border-accent hover:text-accent-ink"
            >
              清除
            </Link>
          )}
        </form>
        {filesError ? (
          <p className="mt-2 text-sm text-mute">
            文件列表加载失败：{filesError}
          </p>
        ) : files.length > 0 ? (
          <>
            <ul className="mt-2 divide-y divide-line-2 rounded-lg border border-line">
              {files.map((f, i) => (
                <li
                  key={i}
                  className="flex items-baseline justify-between gap-3 px-3 py-2 text-xs"
                >
                  <span className="min-w-0 font-mono wrap-anywhere text-ink-2">
                    {f.path}
                  </span>
                  <span className="shrink-0 tabular-nums text-mute">
                    {formatSize(f.size)}
                  </span>
                </li>
              ))}
            </ul>
            <AdminPagination
              basePath={`/admin/torrents/${t.info_hash}`}
              params={fileQuery ? { fq: fileQuery } : {}}
              page={filePage}
              total={filesTotal}
              pageSize={FILES_PAGE_SIZE}
            />
          </>
        ) : (
          <p className="mt-2 text-sm text-mute">
            {fileQuery
              ? `没有文件名包含「${fileQuery}」的文件`
              : "暂无文件列表信息"}
          </p>
        )}

        <p className="mt-4 font-mono text-[11px] leading-relaxed break-all text-faint">
          {t.magnet}
        </p>
      </div>
    </AdminShell>
  );
}
