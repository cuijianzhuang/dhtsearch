import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import AdminPagination from "@/components/admin/AdminPagination";
import BlockedList from "@/components/admin/BlockedList";
import { fetchAdminBlocked } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";
import { formatCount } from "@/lib/format";

export const metadata: Metadata = { title: "黑名单" };

const PAGE_SIZE = 20;

export default async function AdminBlockedPage({
  searchParams,
}: {
  searchParams: Promise<{ page?: string }>;
}) {
  const params = await searchParams;
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);

  const outcome = await adminPageFetch((token) =>
    fetchAdminBlocked(token, page, PAGE_SIZE)
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
    <AdminShell active="blocked">
      <p className="text-sm text-mute">
        共 {formatCount(data.total)} 条。黑名单里的 infohash
        被重新发现时也不会再次收录；取消拉黑后恢复可收录。
      </p>
      <div className="mt-4">
        <BlockedList items={data.results} />
      </div>
      <AdminPagination
        basePath="/admin/blocked"
        params={{}}
        page={data.page}
        total={data.total}
        pageSize={data.page_size}
      />
    </AdminShell>
  );
}
