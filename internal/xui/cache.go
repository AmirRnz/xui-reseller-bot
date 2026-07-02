package xui

import (
	"context"
	"log"
	"sync"
	"time"
)

type InboundCache struct {
	client   *Client
	inbounds []Inbound
	mu       sync.RWMutex
	interval time.Duration
	cancel   context.CancelFunc
	stateMu  sync.Mutex
	running  bool
}

func NewInboundCache(client *Client, interval time.Duration) *InboundCache {
	return &InboundCache{
		client:   client,
		interval: interval,
	}
}

func (c *InboundCache) Start() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.running {
		return
	}
	c.refresh() // Initial load
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true

	ticker := time.NewTicker(c.interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.refresh()
			}
		}
	}()
}

func (c *InboundCache) Stop() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.running = false
}

func (c *InboundCache) refresh() {
	if err := c.client.Login(); err != nil {
		log.Printf("Failed to login to X-UI for cache refresh: %v", err)
		return
	}

	inbounds, err := c.client.GetInbounds()
	if err != nil {
		log.Printf("Failed to fetch inbounds for cache: %v", err)
		return
	}

	c.mu.Lock()
	c.inbounds = inbounds
	c.mu.Unlock()
}

func (c *InboundCache) GetAll() []Inbound {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return a deep copy of the inbounds and client stats to avoid data races
	cpy := make([]Inbound, len(c.inbounds))
	for i, inb := range c.inbounds {
		cpy[i] = inb
		if len(inb.ClientStats) > 0 {
			cpy[i].ClientStats = make([]ClientStat, len(inb.ClientStats))
			copy(cpy[i].ClientStats, inb.ClientStats)
		}
	}
	return cpy
}

func (c *InboundCache) RefreshSync() {
	c.refresh()
}
