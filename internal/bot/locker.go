package bot

import (
	"sync"
)

type KeyLocker struct {
	mu    sync.Mutex
	locks map[string]*refMutex
}

type refMutex struct {
	mu    sync.Mutex
	count int
}

func NewKeyLocker() *KeyLocker {
	return &KeyLocker{
		locks: make(map[string]*refMutex),
	}
}

func (kl *KeyLocker) Lock(key string) func() {
	kl.mu.Lock()
	rm, ok := kl.locks[key]
	if !ok {
		rm = &refMutex{}
		kl.locks[key] = rm
	}
	rm.count++
	kl.mu.Unlock()

	rm.mu.Lock()

	return func() {
		kl.mu.Lock()
		rm.mu.Unlock()
		rm.count--
		if rm.count == 0 {
			delete(kl.locks, key)
		}
		kl.mu.Unlock()
	}
}
