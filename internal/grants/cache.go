package grants

import (
	"container/list"
	"context"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

const (
	maxCacheEntries      = 4096
	maxCacheTTL          = 5 * time.Minute
	maxPendingTouches    = 8192
	defaultFlushInterval = 30 * time.Second
)

type cacheKey struct {
	org      store.OrgID
	serverID string
	userID   string
}

type cachedToken struct {
	token      string
	purpose    string
	credID     string
	expiresAt  time.Time
	cacheUntil time.Time
	rowVersion uint64
}

type cacheItem struct {
	key   cacheKey
	entry cachedToken
}

type tokenLRU struct {
	elems map[cacheKey]*list.Element
	order *list.List
}

func (l *tokenLRU) len() int {
	if l.order == nil {
		return 0
	}
	return l.order.Len()
}

func (l *tokenLRU) get(k cacheKey) (cachedToken, bool) {
	if l.elems == nil {
		return cachedToken{}, false
	}
	el, ok := l.elems[k]
	if !ok {
		return cachedToken{}, false
	}
	l.order.MoveToFront(el)
	return el.Value.(cacheItem).entry, true
}

func (l *tokenLRU) put(k cacheKey, entry cachedToken) {
	if l.elems == nil {
		l.elems = map[cacheKey]*list.Element{}
		l.order = list.New()
	}
	if el, ok := l.elems[k]; ok {
		el.Value = cacheItem{key: k, entry: entry}
		l.order.MoveToFront(el)
		return
	}
	if l.order.Len() >= maxCacheEntries {
		if back := l.order.Back(); back != nil {
			old := l.order.Remove(back).(cacheItem)
			delete(l.elems, old.key)
		}
	}
	l.elems[k] = l.order.PushFront(cacheItem{key: k, entry: entry})
}

func (l *tokenLRU) remove(k cacheKey) {
	if l.elems == nil {
		return
	}
	if el, ok := l.elems[k]; ok {
		l.order.Remove(el)
		delete(l.elems, k)
	}
}

func (l *tokenLRU) removeWhere(pred func(cacheKey) bool) {
	if l.order == nil {
		return
	}
	for el := l.order.Front(); el != nil; {
		next := el.Next()
		it := el.Value.(cacheItem)
		if pred(it.key) {
			l.order.Remove(el)
			delete(l.elems, it.key)
		}
		el = next
	}
}

type pendingTouch struct {
	org      store.OrgID
	serverID string
	userID   string
	at       time.Time
}

func cacheEntryValid(c cachedToken, now time.Time) bool {
	if !now.Before(c.cacheUntil) {
		return false
	}
	if !c.expiresAt.IsZero() && !now.Add(Skew).Before(c.expiresAt) {
		return false
	}
	return true
}

func (s *Service) currentEpoch() uint64 {
	s.mu.Lock()
	e := s.cacheEpoch
	s.mu.Unlock()
	return e
}

func (s *Service) putCachedIfFresh(key cacheKey, c store.Credential, tok upstreamauth.Token, now time.Time, epoch uint64) {
	cacheUntil := now.Add(maxCacheTTL)
	if !tok.Expiry.IsZero() {
		usableUntil := tok.Expiry.Add(-Skew)
		if usableUntil.Before(cacheUntil) {
			cacheUntil = usableUntil
		}
	}
	if !now.Before(cacheUntil) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheEpoch != epoch {
		return
	}
	s.cache.put(key, cachedToken{
		token:      tok.AccessToken,
		purpose:    c.Purpose,
		credID:     c.ID,
		expiresAt:  tok.Expiry,
		cacheUntil: cacheUntil,
		rowVersion: c.RowVersion,
	})
}

func (s *Service) evictCache(org store.OrgID, serverID, userID string) {
	if s == nil {
		return
	}
	key := cacheKey{org: org, serverID: serverID, userID: userID}
	s.mu.Lock()
	s.cacheEpoch++
	s.cache.remove(key)
	s.mu.Unlock()
}

// Evict removes one person's cached token and any queued touch for serverID in org.
func (s *Service) Evict(org store.OrgID, serverID, userID string) {
	if s == nil {
		return
	}
	key := cacheKey{org: org, serverID: serverID, userID: userID}
	s.mu.Lock()
	s.cacheEpoch++
	s.cache.remove(key)
	delete(s.pendingTouches, key)
	s.mu.Unlock()
}

// EvictServer removes every cached token and queued touch for serverID in org.
func (s *Service) EvictServer(org store.OrgID, serverID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cacheEpoch++
	s.cache.removeWhere(func(k cacheKey) bool {
		return k.org == org && k.serverID == serverID
	})
	for k := range s.pendingTouches {
		if k.org == org && k.serverID == serverID {
			delete(s.pendingTouches, k)
		}
	}
	s.mu.Unlock()
}

// EvictUser removes every cached token and queued touch for userID in org.
func (s *Service) EvictUser(org store.OrgID, userID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cacheEpoch++
	s.cache.removeWhere(func(k cacheKey) bool {
		return k.org == org && k.userID == userID
	})
	for k := range s.pendingTouches {
		if k.org == org && k.userID == userID {
			delete(s.pendingTouches, k)
		}
	}
	s.mu.Unlock()
}

func (s *Service) recordTouch(org store.OrgID, serverID, userID string, at time.Time) {
	key := cacheKey{org: org, serverID: serverID, userID: userID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touchClosed {
		return
	}
	if s.pendingTouches == nil {
		s.pendingTouches = map[cacheKey]pendingTouch{}
	}
	if prev, ok := s.pendingTouches[key]; ok {
		if at.After(prev.at) {
			prev.at = at
			s.pendingTouches[key] = prev
		}
		return
	}
	if len(s.pendingTouches) >= maxPendingTouches {
		return
	}
	s.pendingTouches[key] = pendingTouch{org: org, serverID: serverID, userID: userID, at: at}
}

// Start runs the background goroutine that flushes coalesced last_used_at
// updates periodically until ctx is cancelled or Close is called.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		interval := s.FlushInterval
		if interval <= 0 {
			interval = defaultFlushInterval
		}
		s.mu.Lock()
		if s.touchClosed {
			s.mu.Unlock()
			return
		}
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		stop, done := s.stop, s.done
		s.mu.Unlock()

		go func() {
			defer close(done)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					s.Flush(fctx)
					cancel()
					return
				case <-stop:
					return
				case <-t.C:
					fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					s.Flush(fctx)
					cancel()
				}
			}
		}()
	})
}

// Flush writes all pending coalesced last_used_at updates to the store.
func (s *Service) Flush(ctx context.Context) {
	if s == nil || s.Store == nil {
		return
	}
	s.mu.Lock()
	batch := s.pendingTouches
	s.pendingTouches = nil
	s.mu.Unlock()

	for _, pt := range batch {
		if err := s.Store.TouchGrant(ctx, pt.org, pt.serverID, pt.userID, pt.at); err != nil {
			s.logger().Warn("recording a grant's use failed", "server_id", pt.serverID, "err", err)
		}
	}
}

// Close stops the background touch flusher and flushes remaining last_used_at
// updates synchronously. It is safe to call even if Start was never called.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.touchClosed = true
		stop := s.stop
		done := s.done
		s.mu.Unlock()
		if stop != nil {
			close(stop)
			<-done
		}
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Flush(fctx)
	})
}
