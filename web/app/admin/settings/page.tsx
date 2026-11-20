import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import SettingsForm from "@/components/admin/SettingsForm";
import UpdateCard from "@/components/admin/UpdateCard";
import { fetchAdminSettings, fetchAdminUpdate } from "@/lib/admin";
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
  // The update card degrades on its own (it polls and retries), so a
  // failed initial fetch just means "no data yet", never a page error.
  const updateOutcome = await adminPageFetch((token) => fetchAdminUpdate(token));
  return (
    <AdminShell active="settings">
      <UpdateCard initial={updateOutcome.ok ? updateOutcome.data : null} />
      <SettingsForm groups={outcome.data.groups} />
    </AdminShell>
  );
}
