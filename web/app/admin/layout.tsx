import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "管理端",
  robots: { index: false, follow: false },
};

export default function AdminLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return <div className="flex flex-1 flex-col">{children}</div>;
}
