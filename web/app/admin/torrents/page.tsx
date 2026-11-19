import type { Metadata } from "next";
import AdminTorrentBrowser from "@/components/admin/AdminTorrentBrowser";

export const metadata: Metadata = { title: "种子管理" };

export default function AdminTorrentsPage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string; status?: string; page?: string }>;
}) {
  return <AdminTorrentBrowser tab="torrents" searchParams={searchParams} />;
}
