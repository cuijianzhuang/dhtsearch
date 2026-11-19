import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import LlmConsole from "@/components/admin/LlmConsole";
import { fetchAdminModeration, fetchAdminSettings } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";

export const metadata: Metadata = { title: "LLM 审核" };

export default async function AdminLlmPage() {
  const outcome = await adminPageFetch(async (token) => {
    const [settings, moderation] = await Promise.all([
      fetchAdminSettings(token),
      fetchAdminModeration(token),
    ]);
    return { settings, moderation };
  });
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const modGroup = outcome.data.settings.groups.find(
    (g) => g.key === "moderation"
  );
  return (
    <AdminShell active="llm">
      <LlmConsole
        items={modGroup?.items ?? []}
        data={outcome.data.moderation}
      />
    </AdminShell>
  );
}
