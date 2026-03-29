package web

import (
	"encoding/json"
	"os"
	"sync"
)

type AliasStore struct {
	mu      sync.RWMutex
	aliases map[string]string // server name -> display alias
	path    string
}

func NewAliasStore(path string) *AliasStore {
	s := &AliasStore{
		aliases: make(map[string]string),
		path:    path,
	}
	s.load()
	return s
}

func (s *AliasStore) Get(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if alias, ok := s.aliases[name]; ok {
		return alias
	}
	return name
}

func (s *AliasStore) Set(name, alias string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if alias == "" || alias == name {
		delete(s.aliases, name)
	} else {
		s.aliases[name] = alias
	}
	s.save()
}

func (s *AliasStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &s.aliases)
}

func (s *AliasStore) save() {
	data, _ := json.MarshalIndent(s.aliases, "", "  ")
	os.WriteFile(s.path, data, 0644)
}
