import type { Metadata } from "next";
import AdminTorrentBrowser from "@/components/admin/AdminTorrentBrowser";

export const metadata: Metadata = { title: "未审核队列" };

export default function AdminReviewPage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string; status?: string; page?: string }>;
}) {
  return (
    <AdminTorrentBrowser
      tab="review"
      lockedStatus="unreviewed"
      searchParams={searchParams}
    />
  );
}
