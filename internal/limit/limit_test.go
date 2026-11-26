package limit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestGCRAAllowanceRefillAndDisabled(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

	// Zero / negative RPM disables rate limiting.
	var nilLim *RateLimiter
	if ok, wait := nilLim.Allow("k", t0); !ok || wait != 0 {
		t.Fatalf("nil limiter: ok=%v wait=%v", ok, wait)
	}
	if off := NewRateLimiter(0, 0, 0); off != nil {
		t.Fatalf("expected nil limiter when rpm=0, got %+v", off)
	}

	// 60 RPM => 1s emission interval; burst=3 allows 3 immediately.
	lim := NewRateLimiter(60, 3, 100)
	for i := range 3 {
		if ok, wait := lim.Allow("tok1", t0); !ok || wait != 0 {
			t.Fatalf("burst req %d: ok=%v wait=%v", i, ok, wait)
		}
	}
	// 4th request at t0 is rejected with ~1s Retry-After.
	if ok, wait := lim.Allow("tok1", t0); ok || wait != time.Second {
		t.Fatalf("4th req: ok=%v wait=%v, want false, 1s", ok, wait)
	}
	// Another key is unaffected.
	if ok, wait := lim.Allow("tok2", t0); !ok || wait != 0 {
		t.Fatalf("other key: ok=%v wait=%v", ok, wait)
	}
	// At +500ms, still rejected with 500ms remaining.
	if ok, wait := lim.Allow("tok1", t0.Add(500*time.Millisecond)); ok || wait != 500*time.Millisecond {
		t.Fatalf("at +500ms: ok=%v wait=%v, want false, 500ms", ok, wait)
	}
	// At +1s, 1 request is allowed; next one is rejected again.
	if ok, wait := lim.Allow("tok1", t0.Add(time.Second)); !ok || wait != 0 {
		t.Fatalf("at +1s: ok=%v wait=%v", ok, wait)
	}
	if ok, wait := lim.Allow("tok1", t0.Add(time.Second)); ok || wait != time.Second {
		t.Fatalf("second at +1s: ok=%v wait=%v", ok, wait)
	}
	// After full idle period (>3s), full burst of 3 is available again.
	tIdle := t0.Add(10 * time.Second)
	for i := range 3 {
		if ok, _ := lim.Allow("tok1", tIdle); !ok {
			t.Fatalf("after idle req %d rejected", i)
		}
	}
}

func TestLimiterMapIsBounded(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	const capEntries = 10
	// 60 RPM, burst 5 -> emission=1s, tolerance=4s.
	// 1 call at t sets tat = t + 1s (idle/expired once now >= t + 1s).
	// 5 calls at t sets tat = t + 5s (active until t + 5s).
	lim := NewRateLimiter(60, 5, capEntries)

	// Insert 5 keys with 5 calls each (tat = t0 + 5s), and 5 keys with 1 call each (tat = t0 + 1s).
	for i := range 5 {
		kActive := fmt.Sprintf("active-%d", i)
		for range 5 {
			if ok, _ := lim.Allow(kActive, t0); !ok {
				t.Fatalf("%s unexpectedly denied", kActive)
			}
		}
		kIdle := fmt.Sprintf("idle-%d", i)
		if ok, _ := lim.Allow(kIdle, t0); !ok {
			t.Fatalf("%s unexpectedly denied", kIdle)
		}
	}
	if lim.Len() != capEntries {
		t.Fatalf("Len = %d, want %d", lim.Len(), capEntries)
	}

	// Advance clock to t0 + 2s: "idle-*" entries have tat <= now (expired),
	// while "active-*" entries have tat = t0 + 5s > now (still active and exhausted).
	t1 := t0.Add(2 * time.Second)
	if ok, _ := lim.Allow("new-1", t1); !ok {
		t.Fatal("new-1 denied")
	}
	if lim.Len() > capEntries {
		t.Fatalf("Len exceeded cap: %d > %d", lim.Len(), capEntries)
	}
	// Active entries must NOT have been evicted while expired entries existed:
	// active-0 had 5 calls at t0 (tat=t0+5s), at t1=t0+2s allowAt=t0+1s so 1 more is allowed,
	// or actually let's verify active-0 still remembers its state!
	// At t0: 5 calls -> tat = t0+5s. At t1=t0+2s: allowAt = tat-4s = t0+1s <= t1 -> 1 call allowed -> tat=t0+6s,
	// then a 2nd call at t1 has allowAt = t0+2s <= t1 -> allowed -> tat=t0+7s,
	// wait: at t0+2s, allowAt = tat - 4s. For 3 calls, tat reaches t0+8s > t1+4s, so 3rd is denied!
	// If active-0 had been evicted, it would allow all 5 calls at t1!
	allowedActive := 0
	for range 5 {
		if ok, _ := lim.Allow("active-0", t1); ok {
			allowedActive++
		}
	}
	if allowedActive >= 5 {
		t.Fatalf("active-0 was evicted before expired entries (allowed %d/5 at t1)", allowedActive)
	}

	// Flood high-cardinality keys at t1: map size must never exceed capEntries.
	for i := range 200 {
		if ok, _ := lim.Allow(fmt.Sprintf("flood-%d", i), t1); !ok {
			t.Fatalf("flood-%d denied", i)
		}
		if lim.Len() > capEntries {
			t.Fatalf("Len %d exceeded cap %d", lim.Len(), capEntries)
		}
	}
}

func TestInFlightSlotsReleasedOnCancelAndPanic(t *testing.T) {
	// Nil safety
	var nilInf *InFlight
	rel, ok := nilInf.Acquire("org1")
	if !ok {
		t.Fatal("nil Acquire failed")
	}
	rel()
	rel()

	inf := NewInFlight(InFlightConfig{Global: 2, Org: 1, Upstream: 1})

	// Test idempotent release and panic safety on Acquire.
	func() {
		defer func() { _ = recover() }()
		r, acquired := inf.Acquire("orgA")
		if !acquired {
			t.Fatal("orgA first acquire failed")
		}
		defer r()
		// Second concurrent call in orgA must be shed (org cap 1).
		if _, ok := inf.Acquire("orgA"); ok {
			t.Fatal("orgA second acquire should be shed")
		}
		// Different orgB still succeeds (global cap 2, 1 used).
		rB, ok := inf.Acquire("orgB")
		if !ok {
			t.Fatal("orgB acquire failed while global had room")
		}
		// Global cap is now 2/2: orgC must be shed.
		if _, ok := inf.Acquire("orgC"); ok {
			t.Fatal("orgC should be shed at global cap")
		}
		rB()
		rB() // idempotent second call must not underflow
		panic("boom")
	}()

	if inf.Global() != 0 || inf.Org("orgA") != 0 || inf.Org("orgB") != 0 {
		t.Fatalf("slots leaked after panic/double-release: global=%d orgA=%d orgB=%d",
			inf.Global(), inf.Org("orgA"), inf.Org("orgB"))
	}

	// Upstream bulkhead panic & double-release safety.
	func() {
		defer func() { _ = recover() }()
		r, acquired := inf.AcquireUpstream("orgA", "srv1")
		if !acquired {
			t.Fatal("srv1 acquire failed")
		}
		defer r()
		if _, ok := inf.AcquireUpstream("orgA", "srv1"); ok {
			t.Fatal("srv1 second acquire should be shed")
		}
		// Different server in same org succeeds.
		r2, ok := inf.AcquireUpstream("orgA", "srv2")
		if !ok {
			t.Fatal("srv2 acquire failed")
		}
		r2()
		r2()
		panic("upstream panic")
	}()

	if inf.Upstream("orgA", "srv1") != 0 || inf.Upstream("orgA", "srv2") != 0 {
		t.Fatalf("upstream slots leaked: srv1=%d srv2=%d",
			inf.Upstream("orgA", "srv1"), inf.Upstream("orgA", "srv2"))
	}

	// Concurrency stress check under -race.
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, ok := inf.Acquire("orgA"); ok {
				defer r()
				if ur, uok := inf.AcquireUpstream("orgA", "srv1"); uok {
					defer ur()
				}
			}
		}()
	}
	wg.Wait()
	if inf.Global() != 0 || inf.Org("orgA") != 0 || inf.Upstream("orgA", "srv1") != 0 {
		t.Fatalf("slots leaked after concurrent stress: global=%d org=%d up=%d",
			inf.Global(), inf.Org("orgA"), inf.Upstream("orgA", "srv1"))
	}
}

func TestCircuitBreakerOpenHalfOpenClose(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	// Disabled when threshold <= 0.
	if b := NewBreaker(0, 30*time.Second); b != nil {
		t.Fatalf("expected nil breaker when threshold=0, got %+v", b)
	}

	b := NewBreakerWithClock(3, 30*time.Second, clock)

	// 2 failures, then 1 success resets consecutive failure count.
	for range 2 {
		ok, tr, finish := b.Allow("org1", "srv1", "github")
		if !ok || tr != nil {
			t.Fatalf("expected allow in closed state, got ok=%v tr=%v", ok, tr)
		}
		if tr2 := finish(OutcomeFailure); tr2 != nil {
			t.Fatalf("unexpected transition after <threshold failures: %+v", tr2)
		}
	}
	ok, _, finish := b.Allow("org1", "srv1", "github")
	if !ok {
		t.Fatal("expected allow")
	}
	if tr := finish(OutcomeSuccess); tr != nil {
		t.Fatalf("unexpected transition on success: %+v", tr)
	}

	// Now 3 consecutive failures must trip Closed -> Open.
	for i := range 3 {
		ok, _, finish := b.Allow("org1", "srv1", "github")
		if !ok {
			t.Fatalf("call %d should be allowed while closed", i)
		}
		tr := finish(OutcomeFailure)
		if i < 2 && tr != nil {
			t.Fatalf("call %d tripped early: %+v", i, tr)
		}
		if i == 2 && (tr == nil || tr.From != Closed || tr.To != Open) {
			t.Fatalf("call %d transition = %+v, want Closed->Open", i, tr)
		}
	}

	// While Open and within cooldown, calls are rejected; another org's srv1 stays Closed.
	if ok, _, _ := b.Allow("org1", "srv1", "github"); ok {
		t.Fatal("call allowed while Open")
	}
	if ok, _, finish := b.Allow("org2", "srv1", "github"); !ok {
		t.Fatal("org2 was affected by org1's open breaker")
	} else {
		finish(OutcomeSuccess)
	}

	// Advance past cooldown -> next call transitions Open -> HalfOpen as the single probe.
	advance(30 * time.Second)
	ok, tr, finishProbe1 := b.Allow("org1", "srv1", "github")
	if !ok || tr == nil || tr.From != Open || tr.To != HalfOpen {
		t.Fatalf("probe 1: ok=%v tr=%+v, want Open->HalfOpen", ok, tr)
	}
	// Concurrent second call during HalfOpen probe MUST be rejected.
	if ok2, _, _ := b.Allow("org1", "srv1", "github"); ok2 {
		t.Fatal("second concurrent probe allowed during HalfOpen")
	}

	// Neutral outcome (e.g. client canceled) releases probe slot, staying HalfOpen.
	if trN := finishProbe1(OutcomeNeutral); trN != nil {
		t.Fatalf("neutral outcome transitioned: %+v", trN)
	}
	ok, tr, finishProbe2 := b.Allow("org1", "srv1", "github")
	if !ok || tr != nil {
		t.Fatalf("next probe after neutral should be allowed without Open->HalfOpen transition: ok=%v tr=%+v", ok, tr)
	}

	// Failed probe transitions HalfOpen -> Open for full cooldown.
	if trFail := finishProbe2(OutcomeFailure); trFail == nil || trFail.From != HalfOpen || trFail.To != Open {
		t.Fatalf("failed probe transition = %+v, want HalfOpen->Open", trFail)
	}
	if ok, _, _ := b.Allow("org1", "srv1", "github"); ok {
		t.Fatal("call allowed immediately after HalfOpen->Open")
	}

	// Advance another cooldown -> HalfOpen probe succeeds -> HalfOpen -> Closed.
	advance(30 * time.Second)
	ok, tr, finishProbe3 := b.Allow("org1", "srv1", "github")
	if !ok || tr == nil || tr.From != Open || tr.To != HalfOpen {
		t.Fatalf("probe 3: ok=%v tr=%+v, want Open->HalfOpen", ok, tr)
	}
	if trWin := finishProbe3(OutcomeSuccess); trWin == nil || trWin.From != HalfOpen || trWin.To != Closed {
		t.Fatalf("succeeded probe transition = %+v, want HalfOpen->Closed", trWin)
	}
	// Verify States() reports Closed (0) with server alias and no org_id.
	states := b.States()
	if len(states) != 1 || states[0].Server != "github" || states[0].State != Closed {
		t.Fatalf("States = %+v, want [{Server:github State:0}]", states)
	}

	// Verify panic during HalfOpen probe releases probing slot via defer finish(OutcomeNeutral),
	// and multi-org States() aggregates worst state (Open > HalfOpen > Closed).
	for range 3 {
		ok, _, fin := b.Allow("org1", "srv1", "github")
		if !ok {
			t.Fatal("expected allow")
		}
		fin(OutcomeFailure)
	}
	advance(30 * time.Second)
	func() {
		defer func() { _ = recover() }()
		ok, tr, fin := b.Allow("org1", "srv1", "github")
		if !ok || tr == nil || tr.To != HalfOpen {
			t.Fatalf("expected Open->HalfOpen, got ok=%v tr=%+v", ok, tr)
		}
		defer fin(OutcomeNeutral)
		panic("probe panic")
	}()
	// Subsequent probe must NOT be stuck; it can run and succeed to close.
	ok, _, fin2 := b.Allow("org1", "srv1", "github")
	if !ok {
		t.Fatal("breaker remained stuck in HalfOpen probing after panic")
	}
	fin2(OutcomeSuccess)

	// Multi-org aggregation: org1 Open, org2 Closed on same alias "github" -> States returns Open once.
	for range 3 {
		_, _, fin := b.Allow("org1", "srv1", "github")
		fin(OutcomeFailure)
	}
	states = b.States()
	if len(states) != 1 || states[0].Server != "github" || states[0].State != Open {
		t.Fatalf("aggregated States = %+v, want [{Server:github State:Open}]", states)
	}
}
