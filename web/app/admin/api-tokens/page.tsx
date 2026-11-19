import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import ApiTokensManager from "@/components/admin/ApiTokensManager";
import { fetchAdminAPITokens } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";

export const metadata: Metadata = { title: "API 令牌" };

export default async function AdminAPITokensPage() {
  const outcome = await adminPageFetch((token) => fetchAdminAPITokens(token));
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }

  return (
    <AdminShell active="api-tokens">
      <p className="text-sm text-mute">
        给外部客户端调用 /api/search、/api/stats、/api/trending
        的凭证。是否强制要求令牌、以及公开 API
        总开关在「设置 → 公开 API」里；未开启强制时接口保持匿名可用，
        但带有效令牌的请求会被计数。明文只在创建时显示一次，
        后端只存哈希，丢失只能删除重建。
      </p>
      <div className="mt-4">
        <ApiTokensManager items={outcome.data.results} />
      </div>
    </AdminShell>
  );
}
