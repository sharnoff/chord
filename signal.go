package chord

import (
	"context"
	"os"
	ossignal "os/signal" // rename so we can have function args named 'signal'
	"sync"

	"golang.org/x/exp/slices"
)

type SignalManager struct {
	mu sync.Mutex

	parent     *SignalManager
	idInParent int
	children   []*SignalManager

	signals        map[any]signalState
	nextID         int
	stopRequested  bool
	cleanupStarted bool
}

type signalState struct {
	ctx    context.Context
	cancel context.CancelFunc

	callbacks []callback
	cleanup   func()
}

type callback struct {
	id int
	f  func()
}

func NewSignalManager() *SignalManager {
	return &SignalManager{
		signals: make(map[any]signalState),
	}
}

func (m *SignalManager) NewChild() *SignalManager {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopRequested || m.cleanupStarted {
		return m
	}

	id := m.nextID
	m.nextID += 1

	child := &SignalManager{
		parent:     m,
		idInParent: id,
		signals:    make(map[any]signalState),
	}

	m.children = append(m.children, child)
	return child
}

func (m *SignalManager) setupOSSignal(s *signalState, signal any) {
	if s.cleanup != nil {
		return
	}

	if sig, ok := signal.(os.Signal); ok {
		ch := make(chan os.Signal)
		ossignal.Notify(ch, sig)
		s.cleanup = func() {
			ossignal.Stop(ch)
			close(ch)
		}
		go func() {
			for {
				_, ok := <-ch
				if !ok {
					return
				}
				m.Trigger(signal)
			}
		}()
	}
}

func (m *SignalManager) On(signal any, callbacks ...func()) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopRequested || m.cleanupStarted {
		return
	}

	s, _ := m.signals[signal]
	m.setupOSSignal(&s, signal)

	for _, f := range callbacks {
		s.callbacks = append(s.callbacks, callback{id: m.nextID, f: f})
		m.nextID += 1
	}

	m.signals[signal] = s
}

var canceledContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

func (m *SignalManager) Context(signal any) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopRequested || m.cleanupStarted {
		return canceledContext
	}

	s, _ := m.signals[signal]
	if s.ctx != nil {
		return s.ctx
	}

	m.setupOSSignal(&s, signal)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	m.signals[signal] = s
	return s.ctx
}

func (m *SignalManager) Trigger(signal any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopRequested || m.cleanupStarted {
		return
	}

	s, hadEntry := m.signals[signal]
	if s.cancel != nil {
		s.cancel()
	}

	cbIdx := -1
	if len(s.callbacks) != 0 {
		cbIdx = len(s.callbacks) - 1
	}
	childIdx := -1
	if len(m.children) != 0 {
		childIdx = len(m.children) - 1
	}

	for cbIdx >= 0 || childIdx >= 0 {
		cbID := -1
		if cbIdx != -1 {
			cbID = s.callbacks[cbIdx].id
		}
		childID := -1
		if childIdx != -1 {
			childID = m.children[childIdx].idInParent
		}

		if cbID > childID {
			s.callbacks[cbIdx].f()
			cbIdx -= 1
		} else {
			m.children[childIdx].Trigger(signal)
			childIdx -= 1
		}
	}

	if hadEntry {
		delete(m.signals, signal)
	}
}

func (m *SignalManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopRequested || m.cleanupStarted {
		return
	}

	m.stopRequested = true
	m.rectifyStop()
}

func (m *SignalManager) rectifyStop() {
	if !m.stopRequested || len(m.children) != 0 {
		return
	}

	m.cleanupStarted = true
	for _, sigState := range m.signals {
		if sigState.cleanup != nil {
			sigState.cleanup()
		}
	}

	if m.parent != nil {
		m.parent.mu.Lock()
		defer m.parent.mu.Unlock()

		b2i := func(b bool) (i int) {
			if b {
				i = 1
			}
			return
		}

		// Remove the child
		idx, ok := slices.BinarySearchFunc(m.parent.children, m.idInParent, func(c *SignalManager, id int) int {
			return (-1 * b2i(c.idInParent < id)) + b2i(c.idInParent > id)
		})
		if !ok {
			panic("internal error: child SignalManager not found in parent")
		}
		m.parent.children = slices.Delete(m.parent.children, idx, idx+1)

		m.parent.rectifyStop()
	}
}
