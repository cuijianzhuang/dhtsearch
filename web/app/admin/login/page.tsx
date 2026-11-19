import type { Metadata } from "next";
import Link from "next/link";
import LoginForm from "@/components/admin/LoginForm";

export const metadata: Metadata = { title: "管理端登录" };

export default function AdminLoginPage() {
  return (
    <main className="mx-auto flex w-full max-w-md flex-1 flex-col px-4 py-16">
      <div className="rounded-xl border border-line bg-surface p-6 shadow-card">
        <p className="text-xl font-bold tracking-tight">
          DHT<span className="text-accent-ink">Search</span>
          <span className="ml-3 rounded-md bg-accent-soft px-2 py-1 align-middle text-xs font-medium text-accent-ink">
            管理端
          </span>
        </p>
        <LoginForm />
      </div>
      <p className="mt-6 text-center text-xs text-faint">
        <Link href="/" className="inline-flex min-h-11 items-center hover:text-accent-ink">
          返回前台
        </Link>
      </p>
    </main>
  );
}
