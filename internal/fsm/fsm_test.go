package fsm

import (
	"sync"
	"testing"
)

func TestFSM_SetGetClear(t *testing.T) {
	f := NewFSM()
	defer f.Close()

	data := map[string]interface{}{"key": "value"}
	f.SetState(1, "step1", data)

	state := f.GetState(1)
	if state == nil {
		t.Fatal("expected state to not be nil")
	}
	if state.Step != "step1" {
		t.Errorf("expected step1, got %q", state.Step)
	}
	if state.Data["key"] != "value" {
		t.Errorf("expected value, got %v", state.Data["key"])
	}

	// Verify deep copying: modifying data shouldn't affect retrieved state
	data["key"] = "new_value"
	state2 := f.GetState(1)
	if state2.Data["key"] != "value" {
		t.Errorf("expected original value to be unchanged, got %v", state2.Data["key"])
	}

	// Verify ClearState
	f.ClearState(1)
	if f.GetState(1) != nil {
		t.Error("expected state to be cleared")
	}
}

func TestFSM_CompareAndClearState(t *testing.T) {
	f := NewFSM()
	defer f.Close()

	f.SetState(1, "step1", map[string]interface{}{})

	// Match wrong step
	if f.CompareAndClearState(1, "step2") {
		t.Error("expected CompareAndClearState to fail for step2")
	}

	// State should still exist
	if f.GetState(1) == nil {
		t.Error("expected state to still exist")
	}

	// Match correct step
	if !f.CompareAndClearState(1, "step1") {
		t.Error("expected CompareAndClearState to succeed for step1")
	}

	// State should be cleared
	if f.GetState(1) != nil {
		t.Error("expected state to be cleared")
	}
}

func TestFSM_Concurrency(t *testing.T) {
	f := NewFSM()
	defer f.Close()

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			f.SetState(id, "step", map[string]interface{}{"id": id})
			_ = f.GetState(id)
			f.ClearState(id)
		}(int64(i))
	}
	wg.Wait()
}
