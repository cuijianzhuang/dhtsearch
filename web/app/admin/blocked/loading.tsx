import AdminShell from "@/components/admin/AdminShell";
import { AdminListSkeleton } from "@/components/Skeletons";

export default function AdminBlockedLoading() {
  return (
    <AdminShell active="blocked">
      <AdminListSkeleton />
    </AdminShell>
  );
}
