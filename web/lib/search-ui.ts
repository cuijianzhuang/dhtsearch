// Search filter constants shared by server pages and client
// components. Kept out of lib/api.ts on purpose: that module imports
// next/headers (server-only), so anything a client component imports
// at runtime must live here instead.

/** Category keys in tab order, with their Chinese labels. The keys
 *  mirror the backend's store.CategoryOf classification. */
export const CATEGORIES: { key: string; label: string }[] = [
  { key: "video", label: "视频" },
  { key: "audio", label: "音频" },
  { key: "image", label: "图片" },
  { key: "doc", label: "文档" },
  { key: "software", label: "软件" },
  { key: "archive", label: "压缩包" },
  { key: "other", label: "其他" },
];

export type SearchSort = "relevance" | "newest" | "size";

export const SORTS: { key: SearchSort; label: string }[] = [
  { key: "relevance", label: "默认排序" },
  { key: "newest", label: "最新收录" },
  { key: "size", label: "体积最大" },
];
