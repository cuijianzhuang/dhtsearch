import AdminShell from "@/components/admin/AdminShell";
import { AdminListSkeleton } from "@/components/Skeletons";

export default function AdminReportsLoading() {
  return (
    <AdminShell active="reports">
      <AdminListSkeleton />
    </AdminShell>
  );
}
