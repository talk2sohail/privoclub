package policy

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestFlowWindowParsing(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"10s", 10 * time.Second},
		{"10m", 10 * time.Minute},
		{" 1h30m ", 90 * time.Minute},
		{"24h", 24 * time.Hour},
	} {
		got, err := ParseFlowWindow(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ParseFlowWindow(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "0s", "-5m", "9s", "24h1s", "48h", "forever"} {
		if _, err := ParseFlowWindow(bad); err == nil {
			t.Errorf("ParseFlowWindow(%q) succeeded, want error", bad)
		}
	}
}

func TestFlowRuleExpiresAfterWindow(t *testing.T) {
	tr := NewFlowTracker()
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	tr.RecordSource("org-a", "user:u1", "claude", "crm", "get_customer", t0)

	// Just before expiry: tainted.
	tainted, src, age := tr.CheckSink("org-a", "user:u1", "claude", 10*time.Minute, t0.Add(9*time.Minute+59*time.Second))
	if !tainted || src != "crm/get_customer" || age != 9*time.Minute+59*time.Second {
		t.Fatalf("before expiry: tainted=%v src=%q age=%v", tainted, src, age)
	}
	// At exact boundary (age == window): expired.
	if tainted, _, _ := tr.CheckSink("org-a", "user:u1", "claude", 10*time.Minute, t0.Add(10*time.Minute)); tainted {
		t.Fatal("at exact window boundary: expected expired")
	}
	// A later source read refreshes the window and updates source name.
	tr.RecordSource("org-a", "user:u1", "claude", "web", "fetch_page", t0.Add(12*time.Minute))
	tainted, src, age = tr.CheckSink("org-a", "user:u1", "claude", 10*time.Minute, t0.Add(15*time.Minute))
	if !tainted || src != "web/fetch_page" || age != 3*time.Minute {
		t.Fatalf("after refresh: tainted=%v src=%q age=%v", tainted, src, age)
	}
}

func TestFlowRuleIsolatedByOrgAndUser(t *testing.T) {
	tr := NewFlowTracker()
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	tr.RecordSource("org-a", "user:ada", "claude", "crm", "read_note", t0)
	tr.RecordSource("org-a", "token:t1", "", "web", "fetch", t0)

	// Exact matching keys are tainted.
	if tainted, _, _ := tr.CheckSink("org-a", "user:ada", "claude", 10*time.Minute, t0.Add(time.Minute)); !tainted {
		t.Fatal("expected org-a/user:ada/claude tainted")
	}
	if tainted, _, _ := tr.CheckSink("org-a", "token:t1", "", 10*time.Minute, t0.Add(time.Minute)); !tainted {
		t.Fatal("expected org-a/token:t1/'' tainted")
	}

	// Different org, different user, different token, or different client must be isolated.
	for _, tc := range []struct {
		name             string
		org, key, client string
	}{
		{"other org", "org-b", "user:ada", "claude"},
		{"other user", "org-a", "user:bob", "claude"},
		{"other client", "org-a", "user:ada", "cursor"},
		{"pat vs oauth client", "org-a", "user:ada", ""},
		{"other token", "org-a", "token:t2", ""},
		{"user vs token id collision", "org-a", "user:t1", ""},
	} {
		if tainted, src, _ := tr.CheckSink(tc.org, tc.key, tc.client, 10*time.Minute, t0.Add(time.Minute)); tainted {
			t.Errorf("%s was falsely tainted (source=%q)", tc.name, src)
		}
	}
}

func TestFlowRuleBoundedMemoryFailsClosed(t *testing.T) {
	t.Run("per-org cap evicts and fails closed for active window", func(t *testing.T) {
		var tr FlowTracker // also verify zero-value initialization
		t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

		// Fill org-a up to maxPerOrgTracked.
		for i := range maxPerOrgTracked {
			tr.RecordSource("org-a", fmt.Sprintf("user:u-%d", i), "claude", "crm", "read", t0.Add(time.Duration(i)*time.Millisecond))
		}
		if tr.orgs["org-a"].Len() != maxPerOrgTracked || len(tr.entries) != maxPerOrgTracked {
			t.Fatalf("unexpected count before overflow: org=%d total=%d", tr.orgs["org-a"].Len(), len(tr.entries))
		}

		// Before overflow, an unrelated caller in org-a is clean.
		if tainted, _, _ := tr.CheckSink("org-a", "user:bystander", "claude", 10*time.Minute, t0.Add(5*time.Second)); tainted {
			t.Fatal("bystander tainted before overflow")
		}

		// Exceed per-org cap: oldest caller (u-0) is evicted and org-a marks overflow.
		tOverflow := t0.Add(10 * time.Second)
		tr.RecordSource("org-a", "user:u-overflow", "claude", "crm", "read", tOverflow)
		if tr.orgs["org-a"].Len() != maxPerOrgTracked {
			t.Fatalf("org-a exceeded cap: got %d, want %d", tr.orgs["org-a"].Len(), maxPerOrgTracked)
		}

		// Evicted caller AND any excess caller in org-a fail closed during the window.
		if tainted, _, _ := tr.CheckSink("org-a", "user:u-0", "claude", 10*time.Minute, tOverflow.Add(time.Second)); !tainted {
			t.Fatal("evicted caller silently dropped taint")
		}
		if tainted, _, _ := tr.CheckSink("org-a", "user:bystander", "claude", 10*time.Minute, tOverflow.Add(time.Second)); !tainted {
			t.Fatal("excess caller did not fail closed on org overflow")
		}
		// Other orgs remain completely isolated.
		if tainted, _, _ := tr.CheckSink("org-b", "user:u-0", "claude", 10*time.Minute, tOverflow.Add(time.Second)); tainted {
			t.Fatal("org-a overflow tainted org-b")
		}
		// After window passes past overflow time, org-a clears.
		if tainted, _, _ := tr.CheckSink("org-a", "user:bystander", "claude", 10*time.Minute, tOverflow.Add(10*time.Minute)); tainted {
			t.Fatal("org-a overflow did not expire after window")
		}
	})

	t.Run("global cap bounds total entries and fails closed on evicted org", func(t *testing.T) {
		tr := NewFlowTracker()
		t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

		// Fill 5 orgs * 2000 callers = 10_000 callers (at global cap).
		for o := range 5 {
			org := fmt.Sprintf("org-%d", o)
			for i := range 2000 {
				tr.RecordSource(org, fmt.Sprintf("user:%d", i), "claude", "crm", "read", t0.Add(time.Duration(o*2000+i)*time.Millisecond))
			}
		}
		if len(tr.entries) != maxTrackedCallers {
			t.Fatalf("total = %d, want %d", len(tr.entries), maxTrackedCallers)
		}

		// Adding a caller in org-5 evicts the globally oldest entry (from org-0).
		tOverflow := t0.Add(30 * time.Second)
		tr.RecordSource("org-5", "user:new", "claude", "crm", "read", tOverflow)
		if len(tr.entries) != maxTrackedCallers {
			t.Fatalf("total exceeded cap: got %d, want %d", len(tr.entries), maxTrackedCallers)
		}
		// Evicted caller in org-0 fails closed.
		if tainted, _, _ := tr.CheckSink("org-0", "user:0", "claude", 10*time.Minute, tOverflow.Add(time.Second)); !tainted {
			t.Fatal("globally evicted entry in org-0 lost taint")
		}
		// Non-evicted org-4 stays accurate without false overflow for untouched callers.
		if tainted, _, _ := tr.CheckSink("org-4", "user:other", "claude", 10*time.Minute, tOverflow.Add(time.Second)); tainted {
			t.Fatal("org-4 falsely marked overflowed")
		}
	})

	t.Run("expired entries older than 24h are pruned without false overflow", func(t *testing.T) {
		tr := NewFlowTracker()
		t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
		for i := range maxPerOrgTracked {
			tr.RecordSource("org-a", fmt.Sprintf("user:old-%d", i), "", "crm", "read", t0)
		}
		tLater := t0.Add(MaxFlowWindow + time.Second)
		tr.RecordSource("org-a", "user:fresh", "", "crm", "read", tLater)
		if tainted, _, _ := tr.CheckSink("org-a", "user:bystander", "", 10*time.Minute, tLater.Add(time.Second)); tainted {
			t.Fatal("pruning expired entries falsely triggered org overflow")
		}
	})
}

func TestFlowTrackerConcurrentAccess(t *testing.T) {
	tr := NewFlowTracker()
	t0 := time.Now()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			org := fmt.Sprintf("org-%d", id%2)
			for i := range 200 {
				key := fmt.Sprintf("user:%d-%d", id, i%50)
				tr.RecordSource(org, key, "claude", "crm", "read", t0.Add(time.Duration(i)*time.Millisecond))
				tr.CheckSink(org, key, "claude", 10*time.Minute, t0.Add(time.Duration(i+1)*time.Millisecond))
			}
		}(g)
	}
	wg.Wait()
	if tainted, _, _ := tr.CheckSink("org-0", "user:0-10", "claude", 10*time.Minute, t0.Add(time.Second)); !tainted {
		t.Fatal("expected concurrent source taint to be preserved")
	}
}
