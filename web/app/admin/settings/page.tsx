import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import SettingsForm from "@/components/admin/SettingsForm";
import { fetchAdminSettings } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";

export const metadata: Metadata = { title: "设置" };

export default async function AdminSettingsPage() {
  const outcome = await adminPageFetch((token) => fetchAdminSettings(token));
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  return (
    <AdminShell active="settings">
      <SettingsForm groups={outcome.data.groups} />
    </AdminShell>
  );
}
