package bot

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyLocker_Serialization(t *testing.T) {
	locker := NewKeyLocker()
	var count int32

	// We'll lock a key, launch a goroutine that tries to lock the same key,
	// and verify that the goroutine is blocked until we release the lock.
	unlock1 := locker.Lock("test_key")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		unlock2 := locker.Lock("test_key")
		defer unlock2()
		atomic.AddInt32(&count, 1)
	}()

	// Sleep to give goroutine time to run and block
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&count) != 0 {
		t.Error("expected goroutine to be blocked, but count was incremented")
	}

	unlock1()
	wg.Wait()

	if atomic.LoadInt32(&count) != 1 {
		t.Error("expected count to be 1 after unlock")
	}
}

func TestKeyLocker_Concurrency(t *testing.T) {
	locker := NewKeyLocker()
	var count int
	var mu sync.Mutex

	// Run 100 concurrent workers trying to acquire lock on the same key and increment a counter.
	// Without serialization on "test_key", this would race.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locker.Lock("test_key")
			defer unlock()

			// Safely increment count. Since locker serializes, we shouldn't have concurrent updates.
			mu.Lock()
			count++
			mu.Unlock()
		}()
	}

	wg.Wait()
	if count != 100 {
		t.Errorf("expected count to be 100, got %d", count)
	}

	// Verify locker map is cleaned up (ref count = 0)
	locker.mu.Lock()
	l := len(locker.locks)
	locker.mu.Unlock()
	if l != 0 {
		t.Errorf("expected locker locks map to be empty, got %d elements remaining", l)
	}
}
