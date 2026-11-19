import type { Metadata } from "next";
import Link from "next/link";
import LoginForm from "@/components/admin/LoginForm";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";
import { fetchAuthConfig } from "@/lib/admin";
import { fetchSiteConfig } from "@/lib/site";

export const metadata: Metadata = { title: "管理端登录" };

export default async function AdminLoginPage({
  searchParams,
}: {
  searchParams: Promise<{ error?: string }>;
}) {
  const [site, auth, params] = await Promise.all([
    fetchSiteConfig(),
    fetchAuthConfig(),
    searchParams,
  ]);
  return (
    <main className="mx-auto flex w-full max-w-md flex-1 flex-col justify-center px-4 py-10">
      <div className="mb-4 flex justify-end">
        <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
      </div>
      <div className="rounded-2xl border border-line bg-surface p-6 shadow-card sm:p-8">
        <div className="flex flex-col items-center text-center">
          <p className="text-2xl font-bold tracking-tight">
            <SiteLogo name={site.name} />
          </p>
          <span className="mt-3 rounded-md bg-accent-soft px-2 py-1 text-xs font-medium text-accent-ink">
            管理端登录
          </span>
          <p className="mt-3 text-sm text-mute">
            使用管理员账号或已配置的登录方式继续
          </p>
        </div>
        <LoginForm auth={auth} errorCode={params.error} />
      </div>
      <p className="mt-6 text-center text-xs text-faint">
        <Link
          href="/"
          className="inline-flex min-h-11 items-center hover:text-accent-ink"
        >
          返回前台
        </Link>
      </p>
    </main>
  );
}
