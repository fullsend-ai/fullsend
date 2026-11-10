package actorcache

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// fakeClock is a settable clock for deterministic expiry tests.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newTestMemory(t *testing.T, maxAge time.Duration) (*Memory, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: t0}
	m := NewMemory(maxAge)
	m.now = clock.Now
	return m, clock
}

func mustActorID(t *testing.T, sourceID string) ActorID {
	t.Helper()
	id, err := NewActorID("github", "github.com", sourceID)
	require.NoError(t, err)
	return id
}

func humanKey(t *testing.T) Key {
	t.Helper()
	return Key{
		Actor:            mustActorID(t, "12345"),
		Kind:             normevent.ActorHuman,
		Tenant:           "org-1",
		Repository:       "R_kgDOExample",
		Policy:           "fullsend-authz",
		PolicyRevision:   "v2-sha256:abc",
		PermissionSource: "github-collaborator",
	}
}

func botKey(t *testing.T) Key {
	t.Helper()
	k := humanKey(t)
	k.Actor = mustActorID(t, "99999")
	k.Kind = normevent.ActorBot
	k.PermissionSource = "bot-registry"
	k.SourceRevision = "registry-rev-7"
	return k
}

func verified(role normevent.ActorRole, start time.Time) Entry {
	return Entry{Role: role, RoleVerified: true, LookupStart: start}
}

func TestNewActorID(t *testing.T) {
	id, err := NewActorID("github", "GitHub.com", "12345")
	require.NoError(t, err)
	assert.Equal(t, ActorID("fullsend:actor:v1:github:github.com:12345"), id)
	assert.True(t, id.valid())

	id, err = NewActorID("gitlab", "gitlab.example.com:8443", "a:b/c é~")
	require.NoError(t, err)
	assert.Equal(t, ActorID("fullsend:actor:v1:gitlab:gitlab.example.com%3A8443:a%3Ab%2Fc%20%C3%A9~"), id)
	assert.True(t, id.valid())

	for _, tc := range []struct{ system, host, source string }{
		{"", "github.com", "1"},
		{"github", "", "1"},
		{"github", "github.com", ""},
	} {
		_, err := NewActorID(tc.system, tc.host, tc.source)
		assert.Error(t, err, "%+v", tc)
	}
}

func TestActorIDValid(t *testing.T) {
	for _, id := range []ActorID{
		"",
		"octocat",
		"fullsend:entity:v1:github:github.com:work_item:I_1",
		"fullsend:actor:v1:github:github.com",
		"fullsend:actor:v1:github::12345",
		"fullsend:actor:v1:github:github.com:1:2",
	} {
		assert.False(t, id.valid(), "%q", id)
	}
}

func TestPutGet_AbsoluteExpiry(t *testing.T) {
	m, clock := newTestMemory(t, HardMaxAge)
	key := humanKey(t)
	require.NoError(t, m.Put(key, verified(normevent.RoleWrite, t0)))

	clock.now = t0.Add(4 * time.Minute)
	got, ok := m.Get(key)
	require.True(t, ok)
	assert.Equal(t, normevent.RoleWrite, got.Role)
	assert.Equal(t, t0, got.LookupStart)

	// The hit at +4m must not extend expiry.
	clock.now = t0.Add(5 * time.Minute)
	_, ok = m.Get(key)
	assert.False(t, ok)
	assert.Zero(t, m.size, "expired entry is removed on read")
}

func TestPut_ExpiryMeasuredFromLookupStart(t *testing.T) {
	m, clock := newTestMemory(t, HardMaxAge)
	key := humanKey(t)
	clock.now = t0.Add(2 * time.Minute) // lookup took two minutes
	require.NoError(t, m.Put(key, verified(normevent.RoleRead, t0)))

	clock.now = t0.Add(5*time.Minute - time.Nanosecond)
	_, ok := m.Get(key)
	assert.True(t, ok)
	clock.now = t0.Add(5 * time.Minute)
	_, ok = m.Get(key)
	assert.False(t, ok)
}

func TestPut_AlreadyExpiredIsNotStored(t *testing.T) {
	m, clock := newTestMemory(t, time.Minute)
	clock.now = t0.Add(time.Minute)
	require.NoError(t, m.Put(humanKey(t), verified(normevent.RoleAdmin, t0)))
	assert.Zero(t, m.size)
}

func TestNewMemory_MaxAge(t *testing.T) {
	t.Run("clamped to hard cap", func(t *testing.T) {
		m, clock := newTestMemory(t, time.Hour)
		assert.Equal(t, HardMaxAge, m.maxAge)
		require.NoError(t, m.Put(humanKey(t), verified(normevent.RoleWrite, t0)))
		clock.now = t0.Add(HardMaxAge)
		_, ok := m.Get(humanKey(t))
		assert.False(t, ok)
	})
	t.Run("shorter platform bound", func(t *testing.T) {
		m, clock := newTestMemory(t, time.Minute)
		require.NoError(t, m.Put(humanKey(t), verified(normevent.RoleWrite, t0)))
		clock.now = t0.Add(59 * time.Second)
		_, ok := m.Get(humanKey(t))
		assert.True(t, ok)
		clock.now = t0.Add(time.Minute)
		_, ok = m.Get(humanKey(t))
		assert.False(t, ok)
	})
	for _, maxAge := range []time.Duration{0, -time.Minute} {
		t.Run(fmt.Sprintf("disabled at %v", maxAge), func(t *testing.T) {
			m, _ := newTestMemory(t, maxAge)
			require.NoError(t, m.Put(humanKey(t), verified(normevent.RoleWrite, t0)))
			_, ok := m.Get(humanKey(t))
			assert.False(t, ok)
			m.Invalidate(humanKey(t).Actor)
			assert.Empty(t, m.invalidated)
			// Validation still applies so caller bugs surface.
			assert.ErrorIs(t, m.Put(humanKey(t), Entry{Role: normevent.RoleWrite, LookupStart: t0}), ErrUnverified)
		})
	}
}

func TestPut_RejectsIneligibleVerdicts(t *testing.T) {
	tests := []struct {
		name  string
		key   func(*testing.T) Key
		entry Entry
		want  error
	}{
		{"unverified", humanKey, Entry{Role: normevent.RoleWrite, LookupStart: t0}, ErrUnverified},
		{"zero lookup start", humanKey, Entry{Role: normevent.RoleWrite, RoleVerified: true}, ErrUncertainAge},
		{"future lookup start", humanKey, verified(normevent.RoleWrite, t0.Add(time.Second)), ErrUncertainAge},
		{"human with bot role", humanKey, Entry{Role: normevent.RoleWrite, BotRole: "coder", RoleVerified: true, LookupStart: t0}, ErrInvalidEntry},
		{"human with empty role", humanKey, verified("", t0), ErrInvalidEntry},
		{"human with unknown role", humanKey, verified("owner", t0), ErrInvalidEntry},
		{"bot with human role", botKey, Entry{Role: normevent.RoleWrite, BotRole: "coder", RoleVerified: true, LookupStart: t0}, ErrInvalidEntry},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestMemory(t, HardMaxAge)
			key := tc.key(t)
			assert.ErrorIs(t, m.Put(key, tc.entry), tc.want)
			_, ok := m.Get(key)
			assert.False(t, ok)
		})
	}
}

func TestPutGet_VerifiedNegativeAndBotVerdicts(t *testing.T) {
	m, _ := newTestMemory(t, HardMaxAge)

	human := humanKey(t)
	require.NoError(t, m.Put(human, verified(normevent.RoleNone, t0)))
	got, ok := m.Get(human)
	require.True(t, ok)
	assert.Equal(t, normevent.RoleNone, got.Role)

	bot := botKey(t)
	require.NoError(t, m.Put(bot, Entry{Role: normevent.RoleNone, BotRole: "coder", RoleVerified: true, LookupStart: t0}))
	got, ok = m.Get(bot)
	require.True(t, ok)
	assert.Equal(t, "coder", got.BotRole)

	noMatch := botKey(t)
	noMatch.Actor = mustActorID(t, "88888")
	require.NoError(t, m.Put(noMatch, verified(normevent.RoleNone, t0)))
	got, ok = m.Get(noMatch)
	require.True(t, ok)
	assert.Empty(t, got.BotRole)
}

func TestKeyValidation(t *testing.T) {
	mutations := map[string]func(*Key){
		"login as actor":      func(k *Key) { k.Actor = "octocat" },
		"missing kind":        func(k *Key) { k.Kind = "" },
		"unknown kind":        func(k *Key) { k.Kind = "team" },
		"missing tenant":      func(k *Key) { k.Tenant = "" },
		"missing repository":  func(k *Key) { k.Repository = "" },
		"missing policy":      func(k *Key) { k.Policy = "" },
		"missing policy rev":  func(k *Key) { k.PolicyRevision = "" },
		"missing perm source": func(k *Key) { k.PermissionSource = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m, _ := newTestMemory(t, HardMaxAge)
			key := humanKey(t)
			mutate(&key)
			assert.ErrorIs(t, m.Put(key, verified(normevent.RoleWrite, t0)), ErrInvalidKey)
			_, ok := m.Get(key)
			assert.False(t, ok)
		})
	}
}

func TestGet_KeysDifferingInAnyDimensionAreDistinct(t *testing.T) {
	m, _ := newTestMemory(t, HardMaxAge)
	base := humanKey(t)
	require.NoError(t, m.Put(base, verified(normevent.RoleAdmin, t0)))

	mutations := map[string]func(*Key){
		"actor":             func(k *Key) { k.Actor = mustActorID(t, "54321") },
		"provider host":     func(k *Key) { k.Actor, _ = NewActorID("github", "ghe.example.com", "12345") },
		"provider system":   func(k *Key) { k.Actor, _ = NewActorID("gitlab", "github.com", "12345") },
		"kind":              func(k *Key) { k.Kind = normevent.ActorBot },
		"tenant":            func(k *Key) { k.Tenant = "org-2" },
		"repository":        func(k *Key) { k.Repository = "R_other" },
		"policy":            func(k *Key) { k.Policy = "other" },
		"policy revision":   func(k *Key) { k.PolicyRevision = "v2-sha256:def" },
		"permission source": func(k *Key) { k.PermissionSource = "owners" },
		"ref":               func(k *Key) { k.Ref = "refs/heads/main" },
		"source revision":   func(k *Key) { k.SourceRevision = "owners-rev-2" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			key := base
			mutate(&key)
			_, ok := m.Get(key)
			assert.False(t, ok)
		})
	}
	_, ok := m.Get(base)
	assert.True(t, ok)
}

func TestInvalidate(t *testing.T) {
	m, clock := newTestMemory(t, HardMaxAge)
	a := humanKey(t)
	aOtherScope := a
	aOtherScope.Repository = "R_other"
	b := humanKey(t)
	b.Actor = mustActorID(t, "54321")
	for _, k := range []Key{a, aOtherScope, b} {
		require.NoError(t, m.Put(k, verified(normevent.RoleWrite, t0)))
	}

	clock.now = t0.Add(time.Minute)
	m.Invalidate(a.Actor)
	_, ok := m.Get(a)
	assert.False(t, ok)
	_, ok = m.Get(aOtherScope)
	assert.False(t, ok)
	_, ok = m.Get(b)
	assert.True(t, ok, "other actors are unaffected")
	assert.Equal(t, 1, m.size)

	// A lookup that started before the invalidation is stale.
	assert.ErrorIs(t, m.Put(a, verified(normevent.RoleWrite, t0.Add(30*time.Second))), ErrInvalidated)
	assert.ErrorIs(t, m.Put(a, verified(normevent.RoleWrite, t0.Add(time.Minute))), ErrInvalidated)

	// A lookup that started after it may be stored.
	clock.now = t0.Add(2 * time.Minute)
	require.NoError(t, m.Put(a, verified(normevent.RoleRead, t0.Add(90*time.Second))))
	got, ok := m.Get(a)
	require.True(t, ok)
	assert.Equal(t, normevent.RoleRead, got.Role)

	// An invalidation recorded with an earlier clock reading does not move
	// the actor's invalidation record backwards.
	clock.now = t0.Add(30 * time.Second)
	m.Invalidate(a.Actor)
	assert.Equal(t, t0.Add(time.Minute), m.invalidated[a.Actor])

	// Invalidating an unknown actor is a no-op on entries.
	m.Invalidate(mustActorID(t, "unknown"))
	assert.Equal(t, 1, m.size)
}

func TestPut_OlderVerdictDoesNotReplaceNewer(t *testing.T) {
	m, clock := newTestMemory(t, HardMaxAge)
	key := humanKey(t)
	clock.now = t0.Add(2 * time.Minute)
	require.NoError(t, m.Put(key, verified(normevent.RoleAdmin, t0.Add(time.Minute))))
	require.NoError(t, m.Put(key, verified(normevent.RoleRead, t0)))
	got, ok := m.Get(key)
	require.True(t, ok)
	assert.Equal(t, normevent.RoleAdmin, got.Role)
	assert.Equal(t, 1, m.size)

	require.NoError(t, m.Put(key, verified(normevent.RoleWrite, t0.Add(2*time.Minute))))
	got, ok = m.Get(key)
	require.True(t, ok)
	assert.Equal(t, normevent.RoleWrite, got.Role)
	assert.Equal(t, 1, m.size)
}

func TestPut_SweepsExpiredState(t *testing.T) {
	m, clock := newTestMemory(t, time.Minute)
	key := humanKey(t)
	for i := range minSweepThreshold - 1 {
		k := key
		k.Repository = fmt.Sprintf("R_%d", i)
		require.NoError(t, m.Put(k, verified(normevent.RoleWrite, t0)))
	}
	m.Invalidate(mustActorID(t, "gone"))
	assert.Equal(t, minSweepThreshold-1, m.size)
	assert.Len(t, m.invalidated, 1)

	// Once everything above has expired, the Put that reaches the threshold
	// prunes it and leaves only the fresh entry.
	clock.now = t0.Add(time.Minute)
	fresh := key
	fresh.Actor = mustActorID(t, "fresh")
	for i := range 2 {
		k := fresh
		k.Repository = fmt.Sprintf("R_fresh_%d", i)
		require.NoError(t, m.Put(k, verified(normevent.RoleWrite, clock.now)))
	}
	assert.Equal(t, 2, m.size)
	assert.Len(t, m.entries, 1)
	assert.Empty(t, m.invalidated)
	assert.Equal(t, minSweepThreshold, m.sweepAt)
}
