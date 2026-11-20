import AdminShell from "@/components/admin/AdminShell";
import { AdminListSkeleton } from "@/components/Skeletons";

export default function AdminReviewLoading() {
  return (
    <AdminShell active="review">
      <AdminListSkeleton />
    </AdminShell>
  );
}
