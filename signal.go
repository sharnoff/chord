package chord

import (
	"context"
	"errors"
	"os"
	ossignal "os/signal" // rename so we can have function args named 'signal'
	"sync"

	"golang.org/x/exp/slices"
)

// SignalManager provides generic handling for program lifetime signals, where signals are generally
// expected to occur exactly once, and may be user-defined or builtin (like [os.Signal]).
//
// The basic interface is that callbacks are registered with [SignalManager.On], eventually called
// when [SignalManager.TriggerAndWait] is called with that signal.
//
// SignalManager is hierarchical: [SignalManager.NewChild] returns a new SignalManager that inherits
// all signals from its parent, but can be independently triggered without affecting the parent.
//
// All SignalManager methods are safe for multi-threaded use.
//
// Refer to the examples for some sample use cases.
type SignalManager struct {
	mu sync.Mutex

	parent     *SignalManager
	idInParent int
	children   []*SignalManager

	signals map[any]signalState
	nextID  int

	// stopRequested is true if Stop has been called on this SignalManager. Any cleanup callbacks
	// for the signals will be called only once all this SignalManager's children have *also* been
	// stopped.
	stopRequested bool
}

// SignalRegister represents types that can register a signal callback. It is produced exclusively
// by SignalManager.WithErrorHandler and further calls to WithErrorHandler on itself.
//
// Refer to [SignalManager.WithErrorHandler] for more information.
type SignalRegister interface {
	// See [SignalManager.On]
	On(signal any, immediateCtx context.Context, callbacks ...func(context.Context) error) error
	// See [SignalManager.WithErrorHandler]
	WithErrorHandler(handler func(context.Context, error) error) SignalRegister
}

type signalRegisterWithErrorHandler struct {
	base       *SignalManager
	r          SignalRegister
	errHandler func(context.Context, error) error
}

type signalState struct {
	ctx *contextInfo

	callbacks []callback
	cleanup   func()

	triggeredMode signalTriggeredMode
	// observed is true iff triggeredDepth != nil and we've somehow indicated to the user that the
	// signal has been triggered - either by Wait's channel closing, or started calling callbacks,
	// or otherwise.
	//
	// Basically, it prevents calls to Ignore from resetting this signal.
	observed bool
	// ignored is true if Ignore has been called for this signal and observed is not already true.
	// If true, triggeredDepth cannot be a non-nil value other than this SignalManager's depth.
	ignored bool

	// complete may be nil, and is closed exactly when all callbacks for this signal have completed
	// or failed and all children appropriately triggered.
	complete chan struct{}
}

type callback struct {
	id    int
	f     func(context.Context) error
	onErr func(context.Context, error) error
}

type signalTriggeredMode int

const (
	signalTriggeredModeNotTriggered signalTriggeredMode = 0b00

	signalTriggeredModeByParent signalTriggeredMode = 0b01
	signalTriggeredModeBySelf   signalTriggeredMode = 0b10

	signalTriggeredModeMask signalTriggeredMode = 0b11 //nolint:unused
)

type contextInfo struct {
	ctx    context.Context
	cancel context.CancelFunc
}

var alwaysCanceled = func() *contextInfo {
	var c = &contextInfo{}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.cancel()
	return c
}()

// NewSignalManager returns a new SignalManager
//
// Callers MUST make sure to call [SignalManager.Stop] when done, or else the SignalManager may leak
// goroutines.
func NewSignalManager() *SignalManager {
	return &SignalManager{
		signals: make(map[any]signalState),
	}
}

// requires holding the lock
func (m *SignalManager) stopped() bool {
	return m.stopRequested && len(m.children) == 0
}

func (m signalTriggeredMode) bySelf() bool {
	return m&signalTriggeredModeBySelf != 0
}

func (m signalTriggeredMode) byParent() bool {
	return m&signalTriggeredModeByParent != 0
}

func (s signalState) triggered() bool {
	return s.triggeredMode.bySelf() || (s.triggeredMode.byParent() && !s.ignored)
}

func (s signalState) triggeredExclusivelyByParent() bool {
	return s.triggeredMode == signalTriggeredModeByParent
}

func (s signalState) triggeredAndCannotReset() bool {
	return s.triggeredMode.bySelf() || s.observed // s.observed implies s.triggeredMode.atAll()
}

// SignalManagerAlreadyStopped is the payload for a variety of panics that may occur when calling a
// SignalManager's methods after it's already stopped.
//
// See [SignalManager.Stop] for more info.
var SignalManagerAlreadyStopped = errors.New("SignalManager already stopped")

// NewChild creates a new [SignalManager] as a child of m. By default, it will inherit all signals
// that are not later ignored (via [SignalManager.Ignore]). Calling [SignalManager.TriggerAndWait]
// on the child will not affect the parent.
//
// Callers MUST make sure to call [SignalManager.Stop] when done with the child, or else the
// SignalManager may leak goroutines.
//
// NewChild will panic with [SignalManagerAlreadyStopped] if called on a SignalManager that has
// already stopped (see [SignalManager.Stop] for more info).
func (m *SignalManager) NewChild() *SignalManager {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped() {
		panic(SignalManagerAlreadyStopped)
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
		if state.triggered() {
			child.signals[sig] = signalState{triggeredMode: signalTriggeredModeByParent, complete: alwaysClosed}
		}
	}

	m.children = append(m.children, child)
	return child
}

func (m *SignalManager) setupOSSignal(s *signalState, signal any, errHandler func(context.Context, error) error) {
	if s.triggeredAndCannotReset() {
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
			_, ok := <-ch
			if ok {
				ctx := context.Background()
				err := m.TriggerAndWait(signal, ctx)
				if err != nil && errHandler != nil {
					_ = errHandler(ctx, err)
				}
			}
		}()
	}
}

// caller must be holding m.mu
func (m *SignalManager) observeIfTriggered(signal any) (triggered bool) {
	s, _ := m.signals[signal]
	if !s.triggered() {
		return false
	} else if s.triggeredAndCannotReset() {
		return true
	}

	// s.triggered() and may reset. marking it as observed will prevent that reset. We don't want to
	// do that if a parent has since

	if !s.ignored && m.parent != nil && s.triggeredExclusivelyByParent() {
		m.parent.mu.Lock()
		defer m.parent.mu.Unlock()

		triggered := m.parent.observeIfTriggered(signal)
		if !triggered {
			s.triggeredMode = signalTriggeredModeNotTriggered
			m.signals[signal] = s
			return false
		}
	}

	s.observed = true
	m.signals[signal] = s
	return true
}

// On registers the callbacks to be called when the signal is triggered.
//
// If the signal has already been triggered (either via a call to [SignalManager.TriggerAndWait] on
// this SignalManager, or one of its ancestors), then the callbacks will be immediately called,
// passing immediateCtx and returning any unhandled error.
//
// Ordinarily, any unhandled error from a callback will abort executing all other callbacks. A
// function may be provided to handle (and possibly silence) these errors, via
// [SignalManager.WithErrorHandler], which returns an object with the same definition of On.
//
// On will panic with [SignalManagerAlreadyStopped] called on a SignalManager that has already
// stopped (see [SignalManager.Stop] for more info).
//
// All registered callbacks for a signal are called in reverse order - both within and across calls
// to On.
//
// A child SignalManager's callbacks are all called at the same time, as if they were all added at
// the same time, even if adding them was actually interleaved. For example:
//
//	func orderingExample() {
//		mgr := chord.NewSignalManager()
//		defer mgr.Close()
//
//		_ = mgr.On("sig", context.TODO(), func(context.Context) error {
//			fmt.Print("A")
//			return nil
//		})
//
//		child :- mgr.NewChild()
//		defer child.Close()
//
//		_ = child.On("sig", context.TODO() func(context.Context) error {
//			fmt.Print("B")
//			return nil
//		})
//
//		_ = mgr.On("sig", context.TODO(), func(context.Context) error {
//			fmt.Print("C")
//			return nil
//		})
//
//		// Even though this call to On is "after" the previous, the callback is handled later
//		// because the original child was created before.
//		_ = child.On("sig", context.TODO() func(context.Context) error {
//			fmt.Print("D")
//			return nil
//		})
//
//		mgr.TriggerAndWait("sig", context.TODO())
//		// Outputs: CDBA
//	}
//
// It may not be immediately obvious why this even *should* be the desired behavior: It's both for
// efficiency of implementation (no global lists!) and so that, once created, a child SignalManager
// can have callbacks registered asynchronously while still being treated as a complete unit, to be
// handled as one bloc.
//
// See also: [SignalManager.WithErrorHandler].
func (m *SignalManager) On(signal any, immediateCtx context.Context, callbacks ...func(context.Context) error) error {
	return m.on(signal, immediateCtx, nil, callbacks...)
}

// WithErrorHandler returns a SignalRegister wrapped with the error handler. When executing any
// callbacks registered wth [SignalRegister.On] from the returned object, handler will be called
// with any non-nil error.
//
// If handler returns nil, the error will be considered "resolved", and have no further impact. If
// handler returns a non-nil error, that error will be passed through any other previously defined
// error handlers, and eventually returned to the originating [SignalManager.TriggerAndWait], if it is
// not resolved.
func (m *SignalManager) WithErrorHandler(handler func(context.Context, error) error) SignalRegister {
	return &signalRegisterWithErrorHandler{
		base:       m,
		r:          m,
		errHandler: handler,
	}
}

// See [SignalManager.On]
func (r *signalRegisterWithErrorHandler) On(signal any, ctx context.Context, callbacks ...func(context.Context) error) error {
	return r.base.on(signal, ctx, r.errHandler, callbacks...)
}

// See [SignalManager.WithErrorHandler].
//
// Calling WithErrorHandler multiple types will preserve earlier error handlers, calling each
// earlier handler until one returns a nil error.
func (r *signalRegisterWithErrorHandler) WithErrorHandler(handler func(context.Context, error) error) SignalRegister {
	if r.errHandler == nil {
		return &signalRegisterWithErrorHandler{base: r.base, r: r.r, errHandler: handler}
	}

	return &signalRegisterWithErrorHandler{
		base: r.base,
		r:    r,
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

	if m.stopped() {
		return nil
	}

	if len(callbacks) > 0 {
		m.observeIfTriggered(signal)
	}

	s, _ := m.signals[signal]
	m.setupOSSignal(&s, signal, errHandler)

	// if the signal already happened, do the callbacks ourselves, right now
	if s.triggered() {
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

// Context returns a Context that is canceled once the signal is triggered.
//
// Note that the semantics of this method are different from [SignalManager.Wait]; the channel
// returned by Wait is closed after all callbacks have finished, whereas the channel returned by
// Context().Done() is closed after the signal is initially triggered.
func (m *SignalManager) Context(signal any) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped() {
		panic(SignalManagerAlreadyStopped)
	}

	m.observeIfTriggered(signal)

	s, _ := m.signals[signal]
	if s.triggered() && s.ctx == nil {
		s.ctx = alwaysCanceled
		m.signals[signal] = s
	}

	if s.ctx != nil {
		return s.ctx.ctx
	}

	m.setupOSSignal(&s, signal, nil)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = &contextInfo{ctx, cancel}
	m.signals[signal] = s
	return s.ctx.ctx
}

// TriggerAndWait triggers the signal, calling all previously registered callbacks in reverse order,
// returning early if an unhandled error occurs.
//
// When TriggerAndWait returns, any channel returned by [SignalManager.Wait] is guaranteed to be
// closed.
func (m *SignalManager) TriggerAndWait(signal any, ctx context.Context) error {
	return m.triggerAndWaitInner(signal, ctx, false)
}

func (m *SignalManager) triggerAndWaitInner(signal any, ctx context.Context, inherited bool) error {
	triggered, err := m.triggerInner(signal, ctx, inherited)
	if err != nil {
		return err
	}
	if triggered {
		<-m.Wait(signal)
	}
	return nil
}

func (m *SignalManager) triggerInner(signal any, ctx context.Context, inherited bool) (triggered bool, _ error) {
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

	if m.stopped() {
		return false, nil
	}

	s, _ := m.signals[signal]
	if inherited && s.ignored {
		return false, nil
	}

	alreadyTriggered := s.triggered()

	if inherited {
		s.triggeredMode |= signalTriggeredModeByParent
	} else {
		s.triggeredMode |= signalTriggeredModeBySelf
	}
	m.signals[signal] = s

	// if the signal was already triggered, do nothing - but only after updating s.triggeredMode so
	// that we prevent a future Ignore from undoing it
	if alreadyTriggered {
		return true, nil
	}

	cbIdx := -1
	if len(s.callbacks) != 0 {
		cbIdx = len(s.callbacks) - 1
	}
	childIdx := -1
	if len(m.children) != 0 {
		childIdx = len(m.children) - 1
	}

	// trigger any callbacks
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
			err = m.children[childIdx].triggerAndWaitInner(signal, ctx, true)
			childIdx -= 1

		}
		acquire()
	}

	// unset s.callbacks so it can be garbage collected, if need be
	s, _ = m.signals[signal]
	if s.ctx != nil {
		s.ctx.cancel()
	}
	if s.complete != nil {
		close(s.complete)
	}
	s.complete = alwaysClosed
	s.callbacks = nil
	m.signals[signal] = s
	return true, err
}

// Ignore prevents a signal triggering in the SignalManager's parent from affecting it directly.
// If further calls [SignalManager.TriggerAndWait] are made in its parent (or parent's parent, etc),
// then it will not flow down through to this one.
//
// If the signal has been previously triggered in an ancestor SignalManager and *observed* in this
// SignalManager or any descendent, Ignore will have no effect.
//
// A triggered signal is considered "observed" if there has been any call to
// [SignalManager.Context], [SignalManager.Wait], or [SignalManager.On] (with ≥1 callback).
func (m *SignalManager) Ignore(signal any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, _ := m.signals[signal]
	if !s.triggeredAndCannotReset() {
		s.complete = nil // was alwaysClosed, need it to not be.
		s.ignored = true
		m.signals[signal] = s
	}
}

// Stop releases the resources associated with this SignalManager, preventing further signal
// registration or triggering - kind of.
//
// In actuality, freeing the resources (and stopping further signal registration) will only take
// effect once all child SignalManagers have *also* been stopped. In general, this is the state we
// refer to as "stopped" in other documentation. Once a SignalManager is stopped, it is generally an
// error to attempt other operations with it.
func (m *SignalManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopRequested {
		return
	}

	m.stopRequested = true
	m.rectifyStop()
}

func (m *SignalManager) rectifyStop() {
	if !m.stopped() {
		return
	}

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

// Wait returns a channel that is closed once the signal has been triggered *and* all callbacks
// finished or canceled due to an unresolved error. If the signal has already been triggered, Wait
// returns a channel that is already closed.
//
// For convenience, [SignalManager.TryWait] waits with a context.
func (m *SignalManager) Wait(signal any) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.observeIfTriggered(signal)

	s, _ := m.signals[signal]

	// note: s.complete is always non-nil when all signals have completed.
	if s.complete == nil {
		s.complete = make(chan struct{})
		m.signals[signal] = s
	}

	return s.complete
}

// TryWait waits for the signal to be triggered *and* all callbacks finished or canceled due to a
// prior error, unless the context is canceled before that happens.
//
// When called with a context that is already canceled, TryWait will always return the context's
// error.
func (m *SignalManager) TryWait(signal any, ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.Wait(signal):
			return nil
		}
	}
}
