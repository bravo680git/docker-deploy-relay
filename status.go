package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// DeployStatus represents the current state of a deployment.
type DeployStatus string

const (
	StatusRunning DeployStatus = "running"
	StatusSuccess DeployStatus = "success"
	StatusFailed  DeployStatus = "failed"
)

// DeployPhase represents the active step within a running deployment.
type DeployPhase string

const (
	PhasePulling   DeployPhase = "pulling"    // docker pull
	PhaseComposing DeployPhase = "compose_up" // docker compose up -d
)

// DeployResult holds the outcome of a deployment.
type DeployResult struct {
	ID        string       `json:"deploy_id"`
	Project   string       `json:"project"`
	Image     string       `json:"image"`
	Tag       string       `json:"tag"`
	Status    DeployStatus `json:"status"`
	Phase     DeployPhase  `json:"phase,omitempty"`
	Error     string       `json:"error,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	DoneAt    *time.Time   `json:"done_at,omitempty"`
}

const (
	// resultTTL is how long a terminal (success/failed) result is kept after completion.
	resultTTL = 5 * time.Minute
	// maxRunningTTL evicts entries that are stuck in running beyond the global deploy timeout.
	maxRunningTTL = 30 * time.Minute
	// sweepInterval is how often the background sweeper runs.
	sweepInterval = time.Minute
)

// statusStore keeps deployment results in memory.
type statusStore struct {
	mu      sync.Mutex
	results map[string]*DeployResult
}

func newStatusStore() *statusStore {
	s := &statusStore{
		results: make(map[string]*DeployResult),
	}
	go s.sweep()
	return s
}

// sweep periodically removes stale entries to prevent unbounded memory growth.
func (s *statusStore) sweep() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for id, r := range s.results {
			if r.Status != StatusRunning && r.DoneAt != nil && now.Sub(*r.DoneAt) > resultTTL {
				delete(s.results, id)
			} else if r.Status == StatusRunning && now.Sub(r.CreatedAt) > maxRunningTTL {
				delete(s.results, id)
			}
		}
		s.mu.Unlock()
	}
}

// generateID creates a short random hex deploy ID.
func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// fallback: use nanosecond timestamp to avoid collision risk of formatted strings
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Start registers a new running deployment and returns its ID.
func (s *statusStore) Start(p WebhookPayload) string {
	id := generateID()
	now := time.Now()
	s.mu.Lock()
	s.results[id] = &DeployResult{
		ID:        id,
		Project:   p.Project,
		Image:     p.Image,
		Tag:       p.Tag,
		Status:    StatusRunning,
		CreatedAt: now,
	}
	s.mu.Unlock()
	return id
}

// SetPhase updates the active phase of a running deployment.
func (s *statusStore) SetPhase(id string, phase DeployPhase) {
	s.mu.Lock()
	if r, ok := s.results[id]; ok && r.Status == StatusRunning {
		r.Phase = phase
	}
	s.mu.Unlock()
}

// Complete marks a deployment as succeeded.
func (s *statusStore) Complete(id string) {
	now := time.Now()
	s.mu.Lock()
	if r, ok := s.results[id]; ok {
		r.Status = StatusSuccess
		r.DoneAt = &now
	}
	s.mu.Unlock()
}

// Fail marks a deployment as failed with an error message.
func (s *statusStore) Fail(id, errMsg string) {
	now := time.Now()
	s.mu.Lock()
	if r, ok := s.results[id]; ok {
		r.Status = StatusFailed
		r.Error = errMsg
		r.DoneAt = &now
	}
	s.mu.Unlock()
}

// FailIfRunning transitions a deployment to failed only if it is still running.
// This is used as a safety net in deferred cleanup to avoid stuck "running" entries.
func (s *statusStore) FailIfRunning(id, errMsg string) {
	now := time.Now()
	s.mu.Lock()
	if r, ok := s.results[id]; ok && r.Status == StatusRunning {
		r.Status = StatusFailed
		r.Error = errMsg
		r.DoneAt = &now
	}
	s.mu.Unlock()
}

// Get returns the deploy result for a given ID, or nil if not found.
// Terminal results are retained until swept by the background cleaner.
func (s *statusStore) Get(id string) *DeployResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.results[id]
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}
