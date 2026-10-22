package poll

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"strings"
)

// Mixed-version compatibility for label keys.
//
// Pollers from before this driver key a label addition on the issue's Unix
// second (RoutableEvent.LegacyLabelKey) and understand nothing else. This
// version keys an addition on its resource label event ID (or a millisecond
// timestamp). During a rolling deployment, or after a rollback, both versions
// read and write the same state.json, so each representation has to stay
// intelligible to the other:
//
//   - Old reader after new writer. Every dispatch and every retry count this
//     version records for a label addition is also written under the
//     Unix-second key (a "mirror"), so an older poller finds the evidence it
//     knows how to read and neither redispatches the addition nor restarts its
//     retry budget.
//   - New reader after old writer. A Unix-second key nothing mirrors is
//     genuine legacy evidence: it still suppresses the addition (see
//     alreadyDispatched) and its retry count still carries over (see
//     failedCount).
//   - Telling the two apart. A mirror is not proof of anything but the
//     occurrence that wrote it: two distinct additions of one label inside the
//     same second (a remove then re-add) share one second key. Treating every
//     second key as proof would let a mirror of the first addition suppress
//     the second. Each mirror is therefore registered in a ledger that names
//     the occurrence(s) that own it, and a key present in the ledger counts
//     only for its owners.
//
// The ledger travels inside failed_keys_full as legacyMirrorKeyPrefix entries
// (count 1), the same way pending handoffs and replay evidence do: a writer
// from before the ledger existed round-trips unknown failed-key entries
// untouched and re-signs them with the document, so the ledger survives an old
// writer, and the legacy HMAC covers it. If an entry were ever lost, its
// mirror would read as genuine legacy evidence, which errs toward not
// redispatching, never toward a duplicate pipeline.

const legacyMirrorKeyPrefix = "fullsend-legacy-mirror:"

const (
	mirrorKindDispatch = "d"
	mirrorKindFailure  = "f"
)

// mirrorRef records that Legacy, a Unix-second key kept in the dispatched
// (kind "d", full stage:key form) or failed (kind "f") keys, mirrors the
// occurrence key Owner.
type mirrorRef struct {
	Kind   string `json:"m"`
	Legacy string `json:"l"`
	Owner  string `json:"o"`
}

// mirrorSet is the ledger of registered mirrors.
type mirrorSet map[mirrorRef]bool

func (m mirrorSet) add(kind, legacy, owner string) {
	m[mirrorRef{Kind: kind, Legacy: legacy, Owner: owner}] = true
}

// owners lists the occurrence keys that own the mirror key legacy; none means
// legacy is not a registered mirror (genuine legacy evidence).
func (m mirrorSet) owners(kind, legacy string) []string {
	var out []string
	for ref := range m {
		if ref.Kind == kind && ref.Legacy == legacy {
			out = append(out, ref.Owner)
		}
	}
	return out
}

// isMirror reports whether legacy is a registered mirror of any occurrence.
func (m mirrorSet) isMirror(kind, legacy string) bool {
	for ref := range m {
		if ref.Kind == kind && ref.Legacy == legacy {
			return true
		}
	}
	return false
}

func (m mirrorSet) clone() mirrorSet {
	out := make(mirrorSet, len(m))
	for ref := range m {
		out[ref] = true
	}
	return out
}

// ownedBy reports whether legacy is a genuine (unregistered) legacy key, or a
// mirror owned by one of ownerKeys, i.e. whether it counts as evidence about
// the occurrence identified by ownerKeys.
func (m mirrorSet) ownedBy(kind, legacy string, ownerKeys ...string) bool {
	owners := m.owners(kind, legacy)
	if len(owners) == 0 {
		return true
	}
	for _, o := range owners {
		for _, k := range ownerKeys {
			if o == k {
				return true
			}
		}
	}
	return false
}

// eventIdentityKeys lists the keys under which this occurrence's own evidence
// may be recorded: its current key and the millisecond fallback key used
// while its label event ID was unknown.
func eventIdentityKeys(event RoutableEvent) []string {
	keys := []string{event.Key()}
	if fb := event.FallbackLabelKey(); fb != "" {
		keys = append(keys, fb)
	}
	return keys
}

// encodeLegacyMirrors returns state with the ledger written into
// FailedKeysFull as legacyMirrorKeyPrefix entries (replacing any already
// there). The input maps are not modified.
func encodeLegacyMirrors(state persistedPollState) persistedPollState {
	if len(state.LegacyMirrors) == 0 && !hasKeyPrefix(state.FailedKeysFull, legacyMirrorKeyPrefix) {
		return state
	}
	failed := make(map[string]int, len(state.FailedKeysFull)+len(state.LegacyMirrors))
	for k, c := range state.FailedKeysFull {
		if !strings.HasPrefix(k, legacyMirrorKeyPrefix) {
			failed[k] = c
		}
	}
	for ref := range state.LegacyMirrors {
		// Marshalling three strings cannot fail.
		data, _ := json.Marshal(ref)
		failed[legacyMirrorKeyPrefix+base64.RawURLEncoding.EncodeToString(data)] = 1
	}
	if len(failed) == 0 {
		failed = nil
	}
	state.FailedKeysFull = failed
	return state
}

// decodeLegacyMirrors is the inverse of encodeLegacyMirrors.
func decodeLegacyMirrors(state persistedPollState) persistedPollState {
	if !hasKeyPrefix(state.FailedKeysFull, legacyMirrorKeyPrefix) {
		return state
	}
	failed := make(map[string]int, len(state.FailedKeysFull))
	mirrors := make(mirrorSet)
	for k, c := range state.FailedKeysFull {
		if !strings.HasPrefix(k, legacyMirrorKeyPrefix) {
			failed[k] = c
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(k, legacyMirrorKeyPrefix))
		var ref mirrorRef
		if err == nil {
			err = json.Unmarshal(raw, &ref)
		}
		if err != nil || ref.Legacy == "" || ref.Owner == "" || (ref.Kind != mirrorKindDispatch && ref.Kind != mirrorKindFailure) {
			log.Printf("WARNING: dropping malformed legacy-mirror entry in poll state")
			continue
		}
		mirrors[ref] = true
	}
	if len(failed) == 0 {
		failed = nil
	}
	state.FailedKeysFull = failed
	if len(mirrors) > 0 {
		state.LegacyMirrors = mirrors
	}
	return state
}

// recordDispatch records a dispatch of stage for event in keys at ts. For a
// label addition whose key is not already the Unix-second form it also records
// the mirror under that form and registers it to this occurrence in mirrors.
func recordDispatch(keys map[string]int64, mirrors mirrorSet, stage string, event RoutableEvent, ts int64) {
	owner := stage + ":" + event.Key()
	keys[owner] = ts
	legacy := event.LegacyLabelKey()
	if legacy == "" || legacy == event.Key() {
		return
	}
	mirror := stage + ":" + legacy
	if prev, ok := keys[mirror]; !ok || ts > prev {
		keys[mirror] = ts
	}
	if mirrors != nil {
		mirrors.add(mirrorKindDispatch, mirror, owner)
	}
}

// mirrorFailureCount keeps the Unix-second failure key an older poller reads
// in step with this occurrence's retry count, so a rollback keeps the budget.
// With several occurrences sharing the second key, it carries the largest of
// their counts; with none left it is deleted (a 0 tombstone, see
// unionFailedKeys). A second key no ledger entry claims is genuine legacy
// evidence for this occurrence: its count has been carried onto the current
// key by failedCount, and this takes it over.
func mirrorFailureCount(failedKeys map[string]int, mirrors mirrorSet, event RoutableEvent) {
	legacy := event.LegacyLabelKey()
	if legacy == "" || legacy == event.Key() {
		return
	}
	own := eventIdentityKeys(event)
	count := 0
	for _, k := range own {
		if failedKeys[k] > count {
			count = failedKeys[k]
		}
	}
	for _, o := range mirrors.owners(mirrorKindFailure, legacy) {
		isOwn := false
		for _, k := range own {
			if o == k {
				isOwn = true
			}
		}
		if !isOwn && failedKeys[o] > count {
			count = failedKeys[o]
		}
	}
	if count == 0 {
		if _, ok := failedKeys[legacy]; ok || mirrors.isMirror(mirrorKindFailure, legacy) {
			failedKeys[legacy] = 0
		}
		return
	}
	failedKeys[legacy] = count
	if mirrors != nil {
		mirrors.add(mirrorKindFailure, legacy, event.Key())
	}
}

// protectedDispatchKey reports whether a dispatched key must outlive the
// freshness prune because the occurrence it belongs to is still pending
// reconciliation: the occurrence's retry skips already-dispatched stages
// only while that evidence exists.
func protectedDispatchKey(pending map[string]PendingLabel) func(key string) bool {
	if len(pending) == 0 {
		return nil
	}
	suffixes := make(map[string]bool, 3*len(pending))
	for k, pl := range pending {
		if pl.Unresolved {
			continue
		}
		suffixes[k] = true
		ev := pl.routableEvent()
		for _, alt := range []string{ev.Key(), ev.LegacyLabelKey(), ev.FallbackLabelKey()} {
			if alt != "" {
				suffixes[alt] = true
			}
		}
	}
	return func(key string) bool {
		if i := strings.Index(key, ":"); i >= 0 {
			return suffixes[key[i+1:]]
		}
		return false
	}
}
