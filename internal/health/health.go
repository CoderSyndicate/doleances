// Package health tracks the liveness and readiness of a service.
//
// The two mean different things and are deliberately separate: liveness says
// the process is functioning and should not be restarted, readiness says it
// should currently receive traffic. Graceful shutdown works by withdrawing
// readiness first and only then closing the door.
package health

import "sync/atomic"

// State is the liveness and readiness of one service. Safe for concurrent use.
type State struct {
	live  atomic.Bool
	ready atomic.Bool
}

// New returns a state that is alive but not yet ready: a service becomes ready
// once it has finished starting up, never before.
func New() *State {
	s := &State{}
	s.live.Store(true)
	return s
}

// Live reports whether the process is functioning.
func (s *State) Live() bool { return s.live.Load() }

// Ready reports whether the service should receive traffic.
func (s *State) Ready() bool { return s.ready.Load() }

// SetReady opens or closes the service to traffic.
func (s *State) SetReady(ready bool) { s.ready.Store(ready) }

// SetLive marks the process as functioning or not. Clearing it invites a
// restart, so it is for unrecoverable states only.
func (s *State) SetLive(live bool) { s.live.Store(live) }
