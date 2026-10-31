package runtime

import "crypto/sha256"

// progressReconciler owns bounded lifecycle, visibility, run-order and dedup
// state independently of printer/CI output. Callers supply credential redaction
// before any retained display metadata. Parser callbacks remain raw snapshots.
type progressReconciler struct {
	taskProgressSeen   map[taskProgressKey]taskProgressDisplay
	taskProgressRuns   map[[sha256.Size]byte]taskProgressRun
	statusProgressSeen map[[sha256.Size]byte]statusProgressDisplay
}
