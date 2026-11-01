package server

import (
	"context"
	"sync"
)

const credentialService = "dev.orqen.credential"

type CredentialStore interface {
	Put(context.Context, string, string) error
	Get(context.Context, string) (string, bool, error)
	Delete(context.Context, string) error
}
type MemoryStore struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{values: map[string]string{}} }
func (s *MemoryStore) Put(_ context.Context, ref, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[ref] = secret
	return nil
}
func (s *MemoryStore) Get(_ context.Context, ref string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[ref]
	return v, ok, nil
}
func (s *MemoryStore) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref)
	return nil
}
