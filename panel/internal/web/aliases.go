package web

import (
	"log"
	"sync"

	"unilan/internal/db"
)

type AliasStore struct {
	mu      sync.RWMutex
	aliases map[string]string // server name -> display alias
	db      *db.DB
}

func NewAliasStore(database *db.DB) *AliasStore {
	s := &AliasStore{
		aliases: make(map[string]string),
		db:      database,
	}
	loaded, err := database.LoadAliases()
	if err != nil {
		log.Printf("warning: failed to load aliases: %v", err)
	} else {
		s.aliases = loaded
	}
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
	effectiveAlias := alias
	if alias == "" || alias == name {
		effectiveAlias = ""
		delete(s.aliases, name)
	} else {
		s.aliases[name] = alias
	}
	if err := s.db.SetAlias(name, effectiveAlias); err != nil {
		log.Printf("error: failed to persist alias for %q: %v", name, err)
	}
}
