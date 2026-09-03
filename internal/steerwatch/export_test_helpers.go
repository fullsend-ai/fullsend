package steerwatch

import "github.com/fullsend-ai/fullsend/internal/forge"

// MarkSteeredForTest records a delivery batch. It exists so the runner's
// marker logic — which lives in internal/cli and reads Delivered() — can be
// tested without driving a whole poll cycle.
func (w *Watcher) MarkSteeredForTest(messageID int64, runs []forge.WorkflowRun) {
	w.markSteered(messageID, runs, delta{})
}
