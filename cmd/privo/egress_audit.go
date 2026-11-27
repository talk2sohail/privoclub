package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
)

const (
	defaultEgressWindow  = time.Minute
	defaultEgressMaxKeys = 10_000
)

type egressKey struct {
	org    store.OrgID
	caller string
	target string
	reason string
}

// egressRecorder records blocked outbound attempts (F19 / ADR 0008) to the
// audit log and diagnostics log with bounded per-window rate limiting so
// unauthenticated endpoints cannot flood storage or exhaust memory.
type egressRecorder struct {
	Audit       *audit.Writer
	Diagnostics *telemetry.Recorder
	Logger      *slog.Logger
	DefaultOrg  store.OrgID
	Window      time.Duration
	MaxKeys     int
	Now         func() time.Time

	mu   sync.Mutex
	seen map[egressKey]time.Time
}

func (r *egressRecorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *egressRecorder) allow(k egressKey) bool {
	window := r.Window
	if window <= 0 {
		window = defaultEgressWindow
	}
	maxKeys := r.MaxKeys
	if maxKeys <= 0 {
		maxKeys = defaultEgressMaxKeys
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = make(map[egressKey]time.Time)
	}
	if last, ok := r.seen[k]; ok && now.Sub(last) < window {
		return false
	}
	if _, exists := r.seen[k]; !exists && len(r.seen) >= maxKeys {
		for ek, t := range r.seen {
			if now.Sub(t) >= window {
				delete(r.seen, ek)
			}
		}
		if len(r.seen) >= maxKeys {
			return false
		}
	}
	r.seen[k] = now
	return true
}

// Record handles a blocked egress attempt. target must already be strictly
// redacted (scheme://host or IP only).
func (r *egressRecorder) Record(ctx context.Context, target, reason string) {
	if r == nil {
		return
	}
	org := r.DefaultOrg
	actor, actorID, userID, client := "system", "system", "", ""
	callerKey := "system"
	if p, ok := auth.FromContext(ctx); ok {
		if p.OrgID != "" {
			org = p.OrgID
		} else if o, ok := store.OrgFromContext(ctx); ok && o != "" {
			org = o
		}
		if p.Name != "" {
			actor = p.Name
		}
		if id := p.ActorID(); id != "" {
			actorID = id
		} else {
			actorID = actor
		}
		userID = p.UserID
		client = p.Client
		callerKey = actorID
	} else if o, ok := store.OrgFromContext(ctx); ok && o != "" {
		org = o
	}
	if org == "" {
		if r.Logger != nil {
			r.Logger.Warn("egress blocked", "target", target, "reason", reason)
		}
		return
	}
	if !r.allow(egressKey{org: org, caller: callerKey, target: target, reason: reason}) {
		return
	}
	if r.Logger != nil {
		r.Logger.Warn("egress blocked", "org", org, "actor", actor, "target", target, "reason", reason)
	}
	if r.Diagnostics != nil {
		r.Diagnostics.Diagnose(org, store.Diagnostic{
			Level:    store.LevelWarn,
			Category: store.CategoryUpstream,
			Server:   target,
			UserID:   userID,
			Message:  fmt.Sprintf("egress blocked to %s: %s", target, reason),
		})
	}
	if r.Audit != nil {
		ev := store.AuditEvent{
			ID:       ids.New(),
			CallID:   ids.New(),
			Time:     r.now(),
			Actor:    actor,
			ActorID:  actorID,
			UserID:   userID,
			Client:   client,
			Server:   target,
			Action:   "egress.blocked:" + reason,
			Decision: store.ActionDeny,
			Outcome:  store.OutcomeRefused,
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := r.Audit.Admin(actx, org, ev); err != nil && r.Logger != nil {
			r.Logger.Error("audit blocked egress", "target", target, "reason", reason, "err", err)
		}
	}
}
