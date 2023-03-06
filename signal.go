package chord

import (
	"context"
	"os"
	ossignal "os/signal" // rename so we can have function args named 'signal'
	"sync"

	"golang.org/x/exp/slices"
)

type SignalRegister interface {
	On(signal any, immediateCtx context.Context, callbacks ...func(context.Context) error) error
	WithErrorHandler(handler func(context.Context, error) error) SignalRegister
}

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

type signalRegisterWithErrorHandler struct {
	r          SignalRegister
	errHandler func(context.Context, error) error
}

type signalState struct {
	ctx    context.Context
	cancel context.CancelFunc

	callbacks        []callback
	cleanup          func()
	triggered        bool
	inheritedTrigger bool
	ignored          bool
}

type callback struct {
	id    int
	f     func(context.Context) error
	onErr func(context.Context, error) error
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

	// Copy in all signals that have already been triggered
	for sig, state := range m.signals {
		if state.triggered {
			child.signals[sig] = signalState{triggered: true, inheritedTrigger: true}
		}
	}

	m.children = append(m.children, child)
	return child
}

func (m *SignalManager) setupOSSignal(s *signalState, signal any) {
	if s.triggered || s.cleanup != nil {
		return
	}

	if sig, ok := signal.(os.Signal); ok {
		ch := make(chan os.Signal, 1)
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
				_ = m.Trigger(signal, context.Background())
			}
		}()
	}
}

func (m *SignalManager) On(signal any, immediateCtx context.Context, callbacks ...func(context.Context) error) error {
	return m.on(signal, immediateCtx, nil, callbacks...)
}

func (m *SignalManager) WithErrorHandler(handler func(context.Context, error) error) SignalRegister {
	return &signalRegisterWithErrorHandler{
		r:          m,
		errHandler: handler,
	}
}

func (r *signalRegisterWithErrorHandler) base() *SignalManager {
	for {
		switch inner := r.r.(type) {
		case *signalRegisterWithErrorHandler:
			r = inner
		case *SignalManager:
			return inner
		default:
			panic("unexpected type")
		}
	}
}

func (r *signalRegisterWithErrorHandler) On(signal any, ctx context.Context, callbacks ...func(context.Context) error) error {
	return r.base().on(signal, ctx, r.errHandler, callbacks...)
}

func (r *signalRegisterWithErrorHandler) WithErrorHandler(handler func(context.Context, error) error) SignalRegister {
	if r.errHandler == nil {
		return &signalRegisterWithErrorHandler{r: r.r, errHandler: handler}
	}

	return &signalRegisterWithErrorHandler{
		r: r,
		errHandler: func(ctx context.Context, err error) error {
			err = handler(ctx, err)
			if err != nil {
				err = r.errHandler(ctx, err)
			}
			return err
		},
	}
}

func (m *SignalManager) on(signal any, ctx context.Context, errHandler func(context.Context, error) error, callbacks ...func(context.Context) error) error {
	m.mu.Lock()
	locked := true
	defer func() {
		if locked {
			m.mu.Unlock()
		}
	}()

	if m.stopRequested || m.cleanupStarted {
		return nil
	}

	s, _ := m.signals[signal]
	m.setupOSSignal(&s, signal)

	// if the signal already happened, do the callbacks ourselves, right now
	if s.triggered {
		locked = false
		m.mu.Unlock()

		for i := len(callbacks) - 1; i >= 0; i -= 1 {
			err := callbacks[i](ctx)
			if err != nil && errHandler != nil {
				err = errHandler(ctx, err)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}

	for _, f := range callbacks {
		s.callbacks = append(s.callbacks, callback{id: m.nextID, f: f, onErr: errHandler})
		m.nextID += 1
	}

	m.signals[signal] = s
	return nil
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
	if s.triggered {
		return canceledContext
	} else if s.ctx != nil {
		return s.ctx
	}

	m.setupOSSignal(&s, signal)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	m.signals[signal] = s
	return s.ctx
}

func (m *SignalManager) Trigger(signal any, ctx context.Context) error {
	return m.triggerInner(signal, ctx, true)
}

func (m *SignalManager) triggerInner(signal any, ctx context.Context, explicit bool) error {
	m.mu.Lock()
	locked := true
	defer func() {
		if locked {
			m.mu.Unlock()
		}
	}()

	// Lock handling so we can release and re-acquire our lock
	acquire := func() {
		m.mu.Lock()
		locked = true
	}
	release := func() {
		locked = false
		m.mu.Unlock()
	}

	if m.stopRequested || m.cleanupStarted {
		return nil
	}

	s, _ := m.signals[signal]
	if s.triggered {
		if s.inheritedTrigger && explicit {
			s.inheritedTrigger = false
			m.signals[signal] = s
		}

		return nil
	} else if s.ignored && !explicit {
		return nil
	}

	if s.cancel != nil {
		s.cancel()
	}

	s.triggered = true // prevents all further writes to the field

	cbIdx := -1
	if len(s.callbacks) != 0 {
		cbIdx = len(s.callbacks) - 1
	}
	childIdx := -1
	if len(m.children) != 0 {
		childIdx = len(m.children) - 1
	}

	var err error
	for err == nil && (cbIdx >= 0 || childIdx >= 0) {
		cbID := -1
		if cbIdx != -1 {
			cbID = s.callbacks[cbIdx].id
		}
		childID := -1
		if childIdx != -1 {
			childID = m.children[childIdx].idInParent
		}

		// release the lock just for the duration of calling the callbacks or child trigger; these
		// might be reentrant, and we don't want to behave badly.
		//
		// Accessing fields of s is still ok, because s.triggered = true prevents other threads from
		// writing to s.
		release()

		if cbID > childID {
			err = s.callbacks[cbIdx].f(ctx)
			if err != nil && s.callbacks[cbIdx].onErr != nil {
				err = s.callbacks[cbIdx].onErr(ctx, err)
			}
			cbIdx -= 1
		} else {
			err = m.children[childIdx].triggerInner(signal, ctx, false)
			childIdx -= 1

		}
		acquire()
	}

	// unset s.callbacks so it can be garbage collected, if need be
	s.callbacks = nil
	m.signals[signal] = s
	return err
}

func (m *SignalManager) Ignore(signal any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, _ := m.signals[signal]
	s.ignored = true
	if s.inheritedTrigger {
		s.triggered = false
	}
	m.signals[signal] = s
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
