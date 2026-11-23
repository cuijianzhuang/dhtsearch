package metadata

// Per-foundation fetch attribution: the by_source breakdown (see
// source_stats.go) says which discovery channel a fetch came
// from; this is the orthogonal cut — which fetch foundation
// (META_FETCHER: anacrolix / native / libtorrent) executed the
// main pipeline's fetches and how they ended. It exists so a
// foundation switch is a measured decision instead of a vibe: the
// three foundations share the Backend contract's counter
// meanings, so their rows are directly comparable.
//
// Only the main pipeline is attributed. The feeds fetcher is a
// separate anacrolix instance by design; its outcomes already
// land in by_source's feed row, and folding them in here would
// quietly change what the "anacrolix" row means whenever the main
// pipeline runs on another foundation.

// The fetch foundations' stable lowercase names — the values
// published in the fetch section's by_backend rows and the middle
// segment of its stats-table keys (see BackendStatKey). They are
// the same strings META_FETCHER takes and selectBackend reports
// as the effective foundation.
const (
	BackendAnacrolix  = "anacrolix"
	BackendNative     = "native"
	BackendLibtorrent = "libtorrent"
)

// AllBackends lists the fetch foundations in canonical order —
// the order by_backend rows are published in everywhere.
func AllBackends() []string {
	return []string{BackendAnacrolix, BackendNative, BackendLibtorrent}
}

// normalizeBackend folds an unknown foundation name into
// anacrolix, the pipeline's fallback foundation: a name that is
// not one of the three can only arrive from a selection that
// degraded, and the anacrolix row is where its fetches belong.
func normalizeBackend(name string) string {
	switch name {
	case BackendAnacrolix, BackendNative, BackendLibtorrent:
		return name
	default:
		return BackendAnacrolix
	}
}

// FetchBackendStats is one fetch foundation's cumulative fetch
// counters for the main pipeline: how many fetches it started
// (Attempts) and how those fetches ended. The fields are the
// per-source counters (FetchSourceStats) summed over sources —
// the backend dimension aggregates the source dimension, so the
// two breakdowns always reconcile.
type FetchBackendStats struct {
	Backend          string
	Attempts         int64
	Fetched          int64
	TimedOut         int64
	Failed           int64
	PrescreenDropped int64
}

// BackendStatKey is the stats-table key the cmd/server stats
// mirror persists one per-foundation fetch counter under, and the
// key the API reads it back from: fetch_backend_<backend>_<counter>,
// e.g. fetch_backend_native_timed_out. The counter segment reuses
// the per-source counter names (SourceCounterAttempts and
// friends), which deliberately match the API payload's field
// names in both breakdowns.
func BackendStatKey(backend, counter string) string {
	return "fetch_backend_" + normalizeBackend(backend) + "_" + counter
}

// BackendTotals folds a backend's per-source counters (its
// SourceStats) into the foundation's single totals row under
// backend — the effective foundation name, so a degraded
// selection (libtorrent requested, anacrolix running) attributes
// its fetches to the foundation that actually executed them.
func BackendTotals(backend string, srcs []FetchSourceStats) FetchBackendStats {
	out := FetchBackendStats{Backend: normalizeBackend(backend)}
	for _, ss := range srcs {
		out.Attempts += ss.Attempts
		out.Fetched += ss.Fetched
		out.TimedOut += ss.TimedOut
		out.Failed += ss.Failed
		out.PrescreenDropped += ss.PrescreenDropped
	}
	return out
}
