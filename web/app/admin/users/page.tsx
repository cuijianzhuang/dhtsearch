import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import UsersManager from "@/components/admin/UsersManager";
import { fetchAdminUsers, fetchMe } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";

export const metadata: Metadata = { title: "账号" };

export default async function AdminUsersPage() {
  const outcome = await adminPageFetch(async (token) => {
    const me = await fetchMe(token);
    const users =
      me.role === "admin" ? (await fetchAdminUsers(token)).users : null;
    return { me, users };
  });
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  return (
    <AdminShell active="users">
      <UsersManager me={outcome.data.me} users={outcome.data.users} />
    </AdminShell>
  );
}
