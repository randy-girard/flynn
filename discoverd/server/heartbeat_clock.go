package server

import (
	"container/heap"
	"sync"
	"time"
)

// heartbeatClock is the leader-local liveness map. It is not Raft state.
// Steady-state expiry peeks the oldest stamp, so a full clock of live
// instances does not walk the Raft mutex every second.
type heartbeatClock struct {
	mu    sync.Mutex
	at    map[instanceKey]time.Time
	index map[instanceKey]int
	heap  []instanceKey
}

func newHeartbeatClock() *heartbeatClock {
	return &heartbeatClock{
		at:    make(map[instanceKey]time.Time),
		index: make(map[instanceKey]int),
	}
}

func (c *heartbeatClock) Len() int { return len(c.heap) }

func (c *heartbeatClock) Less(i, j int) bool {
	return c.at[c.heap[i]].Before(c.at[c.heap[j]])
}

func (c *heartbeatClock) Swap(i, j int) {
	c.heap[i], c.heap[j] = c.heap[j], c.heap[i]
	c.index[c.heap[i]] = i
	c.index[c.heap[j]] = j
}

func (c *heartbeatClock) Push(x interface{}) {
	k := x.(instanceKey)
	c.index[k] = len(c.heap)
	c.heap = append(c.heap, k)
}

func (c *heartbeatClock) Pop() interface{} {
	n := len(c.heap)
	k := c.heap[n-1]
	c.heap = c.heap[:n-1]
	delete(c.index, k)
	return k
}

func (c *heartbeatClock) stamp(k instanceKey, now time.Time) {
	c.mu.Lock()
	c.stampLocked(k, now)
	c.mu.Unlock()
}

func (c *heartbeatClock) stampAll(keys []instanceKey, now time.Time) {
	c.mu.Lock()
	for _, k := range keys {
		c.stampLocked(k, now)
	}
	c.mu.Unlock()
}

func (c *heartbeatClock) stampLocked(k instanceKey, now time.Time) {
	if c.at == nil {
		c.at = make(map[instanceKey]time.Time)
		c.index = make(map[instanceKey]int)
	}
	c.at[k] = now
	if i, ok := c.index[k]; ok {
		heap.Fix(c, i)
		return
	}
	heap.Push(c, k)
}

func (c *heartbeatClock) remove(k instanceKey) {
	c.mu.Lock()
	c.removeLocked(k)
	c.mu.Unlock()
}

func (c *heartbeatClock) removeLocked(k instanceKey) {
	i, ok := c.index[k]
	if !ok {
		delete(c.at, k)
		return
	}
	heap.Remove(c, i)
	delete(c.at, k)
}

func (c *heartbeatClock) removeService(service string) {
	c.mu.Lock()
	var keys []instanceKey
	for k := range c.at {
		if k.service == service {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		c.removeLocked(k)
	}
	c.mu.Unlock()
}

func (c *heartbeatClock) has(k instanceKey) bool {
	c.mu.Lock()
	_, ok := c.at[k]
	c.mu.Unlock()
	return ok
}

func (c *heartbeatClock) get(k instanceKey) (time.Time, bool) {
	c.mu.Lock()
	t, ok := c.at[k]
	c.mu.Unlock()
	return t, ok
}

func (c *heartbeatClock) clear() {
	c.mu.Lock()
	c.at = make(map[instanceKey]time.Time)
	c.index = make(map[instanceKey]int)
	c.heap = nil
	c.mu.Unlock()
}

// expired returns keys whose current stamp is older than ttl. The keys stay
// in the clock so a failed Raft apply can see them again. A fresh oldest
// stamp returns immediately.
func (c *heartbeatClock) expired(now time.Time, ttl time.Duration) []instanceKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []instanceKey
	for len(c.heap) > 0 {
		k := c.heap[0]
		at, ok := c.at[k]
		if !ok || now.Sub(at) <= ttl {
			break
		}
		heap.Pop(c)
		out = append(out, k)
	}
	for _, k := range out {
		heap.Push(c, k)
	}
	return out
}
