package gateway

import (
	"container/list"
	"context"
	"errors"
	"sync"

	"privo.club/privo/internal/store"
)

const maxPolicyCacheOrgs = 1024

type policyVersionStore interface {
	PolicyVersion(ctx context.Context, org store.OrgID) (int64, error)
}

var _ policyVersionStore = (*store.Store)(nil)

type policyCacheEntry struct {
	org     store.OrgID
	version int64
	rules   []store.Policy
	def     string
}

type policyCache struct {
	mu     sync.Mutex
	elems  map[store.OrgID]*list.Element
	order  *list.List
	loadMu [64]sync.Mutex
}

func orgShard(org store.OrgID) int {
	var h uint32 = 2166136261
	for i := 0; i < len(org); i++ {
		h ^= uint32(org[i])
		h *= 16777619
	}
	return int(h % 64)
}

func (c *policyCache) get(org store.OrgID, version int64) ([]store.Policy, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.elems == nil {
		return nil, "", false
	}
	el, ok := c.elems[org]
	if !ok {
		return nil, "", false
	}
	entry := el.Value.(policyCacheEntry)
	if entry.version != version {
		return nil, "", false
	}
	c.order.MoveToFront(el)
	return entry.rules, entry.def, true
}

func (c *policyCache) put(org store.OrgID, version int64, rules []store.Policy, def string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.elems == nil {
		c.elems = map[store.OrgID]*list.Element{}
		c.order = list.New()
	}
	entry := policyCacheEntry{org: org, version: version, rules: rules, def: def}
	if el, ok := c.elems[org]; ok {
		el.Value = entry
		c.order.MoveToFront(el)
		return
	}
	if c.order.Len() >= maxPolicyCacheOrgs {
		if back := c.order.Back(); back != nil {
			old := c.order.Remove(back).(policyCacheEntry)
			delete(c.elems, old.org)
		}
	}
	c.elems[org] = c.order.PushFront(entry)
}

func (g *Gateway) policies(ctx context.Context, org store.OrgID) ([]store.Policy, string, error) {
	pvs, ok := g.Store.(policyVersionStore)
	if !ok {
		rules, err := g.Store.ListPolicies(ctx, org)
		if err != nil {
			return nil, "", err
		}
		def, err := g.Store.Setting(ctx, org, SettingPolicyDefault, store.ActionAllow)
		if err != nil {
			return nil, "", err
		}
		return rules, def, nil
	}

	ver, err := pvs.PolicyVersion(ctx, org)
	if err != nil {
		return nil, "", err
	}
	if rules, def, hit := g.policyCache.get(org, ver); hit {
		return rules, def, nil
	}

	mu := &g.policyCache.loadMu[orgShard(org)]
	mu.Lock()
	defer mu.Unlock()

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			ver, err = pvs.PolicyVersion(ctx, org)
			if err != nil {
				return nil, "", err
			}
		}
		if rules, def, hit := g.policyCache.get(org, ver); hit {
			return rules, def, nil
		}
		rules, err := g.Store.ListPolicies(ctx, org)
		if err != nil {
			return nil, "", err
		}
		def, err := g.Store.Setting(ctx, org, SettingPolicyDefault, store.ActionAllow)
		if err != nil {
			return nil, "", err
		}
		verAfter, err := pvs.PolicyVersion(ctx, org)
		if err != nil {
			return nil, "", err
		}
		if verAfter == ver {
			g.policyCache.put(org, ver, rules, def)
			return rules, def, nil
		}
		ver = verAfter
	}
	return nil, "", errors.New("gateway: policy changed repeatedly while loading")
}
