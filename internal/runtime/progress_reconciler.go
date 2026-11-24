package runtime

import "crypto/sha256"

// progressReconciler owns bounded lifecycle, visibility, run-order and dedup
// state independently of printer/CI output. Callers supply credential redaction
// before any retained display metadata. Parser callbacks remain raw snapshots.
type progressReconciler struct {
	taskProgressSeen   map[taskProgressKey]taskProgressDisplay
	taskProgressRuns   map[[sha256.Size]byte]taskProgressRun
	statusProgressSeen map[[sha256.Size]byte]statusProgressDisplay

	// Admission limits. Zero means the production default; tests in this
	// package set small values so overflow is cheap to exercise.
	taskEntryLimit     int
	agentsPerTaskLimit int
	rowLimit           int
	statusEntryLimit   int
}

func (r *progressReconciler) maxTaskEntries() int {
	return limitOrDefault(r.taskEntryLimit, maxTaskProgressEntries)
}

func (r *progressReconciler) maxAgentsPerTask() int {
	return limitOrDefault(r.agentsPerTaskLimit, maxTaskProgressAgentsPerTask)
}

func (r *progressReconciler) maxRows() int {
	return limitOrDefault(r.rowLimit, maxTaskProgressRows)
}

func (r *progressReconciler) maxStatusEntries() int {
	return limitOrDefault(r.statusEntryLimit, maxStatusProgressEntries)
}

func limitOrDefault(limit, fallback int) int {
	if limit > 0 {
		return limit
	}
	return fallback
}
