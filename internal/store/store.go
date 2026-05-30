// Package store is an in-memory incident store. Production deployments
// would use Postgres or similar; for v0 we use a map guarded by a
// RWMutex.
//
// The store is the only place the data plane and intelligence plane
// share state: the rule engine reads OpenIncidents from here as part
// of enrichment (for the duplicate-detection rule), and the routing
// layer writes finalised incidents here for later inspection.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

type Store struct {
	mu       sync.RWMutex
	byID     map[string]*model.Incident
}

func New() *Store {
	return &Store{byID: make(map[string]*model.Incident)}
}

// NewID picks a stable-ish incident ID from the alert and current time.
// We hash (fingerprint, startsAt) so the same alert re-firing during a
// short window yields the same ID — the duplicate rule depends on this.
func NewID(a *model.Alert) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", a.Fingerprint, a.StartsAt.UTC().Format(time.RFC3339))))
	return "INC-" + hex.EncodeToString(h[:4])
}

func (s *Store) Add(inc *model.Incident) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[inc.ID] = inc
}

func (s *Store) Get(id string) (*model.Incident, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inc, ok := s.byID[id]
	return inc, ok
}

// OpenIncidents returns all unresolved incidents. Cheap copy because
// we don't expect many open at once.
func (s *Store) OpenIncidents() []*model.Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Incident, 0)
	for _, inc := range s.byID {
		if !inc.Resolved {
			out = append(out, inc)
		}
	}
	return out
}

// All returns every incident, newest first. Used by the HTTP API.
func (s *Store) All() []*model.Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Incident, 0, len(s.byID))
	for _, inc := range s.byID {
		out = append(out, inc)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Resolve marks an incident as resolved. Returns false if no such
// incident exists.
func (s *Store) Resolve(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.byID[id]
	if !ok {
		return false
	}
	inc.Resolved = true
	inc.UpdatedAt = time.Now().UTC()
	return true
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}
