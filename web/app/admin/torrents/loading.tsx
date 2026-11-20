import AdminShell from "@/components/admin/AdminShell";
import { AdminListSkeleton } from "@/components/Skeletons";

export default function AdminTorrentsLoading() {
  return (
    <AdminShell active="torrents">
      <AdminListSkeleton />
    </AdminShell>
  );
}
