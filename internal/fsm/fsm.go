package fsm

import (
	"context"
	"sync"
	"time"
)

type State struct {
	Step      string
	Data      map[string]interface{}
	UpdatedAt time.Time
}

type FSM struct {
	mu     sync.RWMutex
	states map[int64]*State
	cancel context.CancelFunc
}

func NewFSM() *FSM {
	ctx, cancel := context.WithCancel(context.Background())
	f := &FSM{
		states: make(map[int64]*State),
		cancel: cancel,
	}
	go f.janitor(ctx)
	return f
}

func (f *FSM) Close() {
	if f.cancel != nil {
		f.cancel()
	}
}

func (f *FSM) janitor(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.evictExpired()
		}
	}
}

func (f *FSM) evictExpired() {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for userID, state := range f.states {
		if state != nil && now.Sub(state.UpdatedAt) > 24*time.Hour {
			delete(f.states, userID)
		}
	}
}

func deepCopyMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		switch m := v.(type) {
		case map[string]interface{}:
			dst[k] = deepCopyMap(m)
		default:
			dst[k] = v
		}
	}
	return dst
}

func (f *FSM) SetState(userID int64, stateOrStep interface{}, data ...map[string]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var state *State
	switch v := stateOrStep.(type) {
	case *State:
		if v == nil {
			delete(f.states, userID)
			return
		}
		state = &State{
			Step:      v.Step,
			Data:      deepCopyMap(v.Data),
			UpdatedAt: time.Now(),
		}
	case State:
		state = &State{
			Step:      v.Step,
			Data:      deepCopyMap(v.Data),
			UpdatedAt: time.Now(),
		}
	case string:
		state = &State{
			Step:      v,
			UpdatedAt: time.Now(),
		}
		if len(data) > 0 && data[0] != nil {
			state.Data = deepCopyMap(data[0])
		}
	default:
		return
	}

	if state.Data == nil {
		state.Data = map[string]interface{}{}
	}
	f.states[userID] = state
}

func (f *FSM) GetState(userID int64) *State {
	f.mu.RLock()
	defer f.mu.RUnlock()

	state, ok := f.states[userID]
	if !ok || state == nil {
		return nil
	}
	return &State{
		Step:      state.Step,
		Data:      deepCopyMap(state.Data),
		UpdatedAt: state.UpdatedAt,
	}
}

func (f *FSM) ClearState(userID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.states, userID)
}

func (f *FSM) CompareAndClearState(userID int64, expectedStep string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	state, ok := f.states[userID]
	if !ok || state == nil {
		return false
	}
	if state.Step == expectedStep {
		delete(f.states, userID)
		return true
	}
	return false
}
