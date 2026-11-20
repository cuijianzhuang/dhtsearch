import { ResultListSkeleton } from "@/components/Skeletons";

// Instant shell for /search while the server component fetches: the
// same column width and header rhythm as the real page, with
// card-shaped placeholders where the results land. Search submits
// are document navigations, so this streams in as the first paint of
// the results document instead of a blank wait on the old page.
export default function SearchLoading() {
  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col px-4 py-6">
      <div className="flex items-center gap-4" aria-hidden>
        <div className="h-7 w-28 shrink-0 animate-pulse rounded-md bg-soft" />
        <div className="h-11 min-w-0 flex-1 animate-pulse rounded-lg bg-soft" />
        <div className="h-11 w-24 shrink-0 animate-pulse rounded-full bg-soft" />
      </div>
      <div
        className="mt-4 h-11 animate-pulse rounded-lg bg-soft"
        aria-hidden
      />
      <div className="mt-4 flex gap-2" aria-hidden>
        {Array.from({ length: 5 }, (_, i) => (
          <div
            key={i}
            className="h-11 w-16 animate-pulse rounded-lg bg-soft"
          />
        ))}
      </div>
      <ResultListSkeleton count={5} />
    </main>
  );
}
