// Package connector is the frame for outside data sources: RSS and web page
// watches, GitHub releases, calendars, price and restock checks, exchange
// rates. A connector fetches items on a schedule (as a module.Job) and hands
// them to the module that owns them; credentials it needs live in the vault.
//
// Nothing implements it yet: the intelligence and calendar modules will. The
// interface is here so they share one shape for configuration, status and
// items, and so the panel and agents can list sources the same way.
package connector

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Item is one thing a source produced: an article, a release, an event, a
// price.
type Item struct {
	// Key is stable per item within its source, so a refetch does not
	// produce duplicates.
	Key   string          `json:"key"`
	Title string          `json:"title"`
	URL   string          `json:"url,omitempty"`
	At    time.Time       `json:"at"`
	Body  string          `json:"body,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Status is a source's health as the panel shows it.
type Status struct {
	LastFetch time.Time `json:"last_fetch"`
	LastError string    `json:"last_error,omitempty"`
	Items     int       `json:"items"`
}

// Connector is a kind of source. Config is the source's own settings (a feed
// URL, a repository); Secret its credential, read from the vault.
type Connector interface {
	ID() string
	Name() string
	// Schema is the JSON Schema of Config.
	Schema() map[string]any
	Fetch(ctx context.Context, config json.RawMessage, secret string) ([]Item, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Connector{}
)

// Register adds a connector kind; registering the same id twice panics.
func Register(c Connector) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[c.ID()]; dup {
		panic("connector: duplicate " + c.ID())
	}
	registry[c.ID()] = c
}

// Get finds a connector kind.
func Get(id string) (Connector, bool) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := registry[id]
	return c, ok
}

// All lists the connector kinds by id.
func All() []Connector {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Connector, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}
