package anthropic

import (
	"sync"
)

type stateStore struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func newStateStore() *stateStore {
	return &stateStore{entries: make(map[string][]byte)}
}

func (s *stateStore) put(key string, blob []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = blob
}

func (s *stateStore) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.entries[key]
	return blob, ok
}
