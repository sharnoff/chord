package chord_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sharnoff/chord"
	"golang.org/x/exp/slices"
)

func TestSignalCallbackOrdering(t *testing.T) {
	t.Parallel()

	var history []int
	record := func(x int) func(context.Context) error {
		return func(context.Context) error {
			history = append(history, x)
			return nil
		}
	}

	sig := "signal"
	mgr := chord.NewSignalManager()
	defer mgr.Stop()

	ctx := context.Background()
	var err error

	err = mgr.On(sig, ctx, record(1))
	assert(err == nil)

	child1 := mgr.NewChild()
	defer child1.Stop()
	err = child1.On(sig, ctx, record(2))
	assert(err == nil)

	err = mgr.On(sig, ctx, record(4))
	assert(err == nil)

	err = child1.On(sig, ctx, record(3))
	assert(err == nil)

	child2 := mgr.NewChild()
	defer child2.Stop()
	err = child2.On(sig, ctx, record(5))
	assert(err == nil)

	_ = mgr.On(sig, ctx, record(6))
	assert(err == nil)

	if err := mgr.Trigger(sig, context.Background()); err != nil {
		t.Fatalf("unexpected error on Trigger: %s", err)
	}

	if !slices.Equal(history, []int{6, 5, 4, 3, 2, 1}) {
		t.Fatalf("bad ordering, got: %v", history)
	}
}

func TestSignalBasicErrorHandling(t *testing.T) {
	t.Parallel()

	sig := "foo"

	mgr := chord.NewSignalManager()
	testErr := errors.New("test")

	expectErr := func(ctx context.Context, err error) error {
		if err == nil {
			panic("expected error")
		}
		return nil
	}

	var history []int
	ctx := context.Background()
	var err error

	err = mgr.WithErrorHandler(expectErr).On(sig, ctx, func(context.Context) error {
		history = append(history, 1)
		return nil
	})
	assert(err == nil)
	err = mgr.On(sig, ctx, func(context.Context) error {
		history = append(history, 2)
		return testErr
	})
	assert(err == nil)
	err = mgr.WithErrorHandler(expectErr).On(sig, ctx, func(context.Context) error {
		history = append(history, 3)
		return nil
	}, func(context.Context) error {
		history = append(history, 4)
		return testErr
	})
	assert(err == nil)
	err = mgr.On(sig, ctx, func(context.Context) error {
		history = append(history, 5)
		return nil
	})
	assert(err == nil)

	err = mgr.Trigger(sig, context.Background())
	if err != testErr {
		t.Fatalf("expected error %v, got %v", testErr, err)
	}

	if !slices.Equal(history, []int{5, 4, 3, 2}) {
		t.Fatalf("bad history: %v", history)
	}

	// Make sure re-triggering doesn't do anything weird
	assert(mgr.Trigger(sig, context.Background()) == nil)
	assert(slices.Equal(history, []int{5, 4, 3, 2}))
}

func TestSignalContext(t *testing.T) {
	t.Parallel()

	foo := "foo"
	bar := "bar"

	var history []int

	type immKey struct{}
	type fooKey struct{}
	type barKey struct{}

	mgr := chord.NewSignalManager()
	immediateCtx := context.WithValue(context.Background(), immKey{}, "value for immediate")

	fooCtx := mgr.Context(foo)
	assert(fooCtx.Value(immKey{}) == nil) // immediate context only applies when callbacks are immediately run

	assert(fooCtx.Err() == nil)

	_ = mgr.On(foo, immediateCtx, func(ctx context.Context) error {
		assert(ctx.Value(fooKey{}) == "value for foo")

		history = append(history, 1)
		return nil
	})

	_ = mgr.On(bar, immediateCtx, func(base context.Context) error {
		assert(base.Value(barKey{}) == "value for bar")

		history = append(history, 2)
		ctx := context.WithValue(base, fooKey{}, "value for foo")

		assert(fooCtx.Err() == nil)
		_ = mgr.Trigger(foo, ctx)
		assert(fooCtx.Err() != nil)

		history = append(history, 3)
		return nil
	})

	barCtx := mgr.Context(bar)

	assert(barCtx.Err() == nil)
	_ = mgr.Trigger(bar, context.WithValue(context.Background(), barKey{}, "value for bar"))
	assert(barCtx.Err() != nil)

	// bar was already triggered; we should be called with immediateCtx
	_ = mgr.On(bar, immediateCtx, func(ctx context.Context) error {
		history = append(history, 4)
		assert(ctx.Value(immKey{}) == "value for immediate")
		return nil
	})
	// same for foo
	_ = mgr.On(foo, immediateCtx, func(ctx context.Context) error {
		history = append(history, 5)
		assert(ctx.Value(immKey{}) == "value for immediate")
		return nil
	})

	expectedHistory := []int{2, 1, 3, 4, 5}
	if !slices.Equal(history, expectedHistory) {
		t.Fatalf("bad history: expected %v but got %v", expectedHistory, history)
	}
}

func TestSignalPastTriggerPreservedOnNew(t *testing.T) {
	t.Parallel()

	foo := "foo"

	mgr := chord.NewSignalManager()
	child1 := mgr.NewChild()

	assert(mgr.Trigger(foo, context.Background()) == nil)

	var history []int
	_ = mgr.On(foo, context.Background(), func(context.Context) error {
		history = append(history, 1)
		return nil
	})
	assert(slices.Equal(history, []int{1}))

	_ = child1.On(foo, context.Background(), func(context.Context) error {
		history = append(history, 2)
		return nil
	})

	assert(slices.Equal(history, []int{1, 2}))

	child2 := mgr.NewChild()
	_ = child2.On(foo, context.Background(), func(context.Context) error {
		history = append(history, 3)
		return nil
	})
	assert(slices.Equal(history, []int{1, 2, 3}))
}

func TestSignalIgnored(t *testing.T) {
	t.Parallel()

	foo := "foo"

	mgr := chord.NewSignalManager()
	child1 := mgr.NewChild()
	child1.Ignore(foo)

	ran := false

	_ = child1.On(foo, context.Background(), func(context.Context) error {
		ran = true
		return nil
	})

	assert(mgr.Trigger(foo, context.Background()) == nil)
	assert(!ran)

	assert(child1.Trigger(foo, context.Background()) == nil)
	assert(ran)

	ran = false
	child2 := mgr.NewChild()
	child2.Ignore(foo)
	assert(child2.Context(foo).Err() == nil)

	_ = child2.On(foo, context.Background(), func(context.Context) error {
		ran = true
		return nil
	})
	assert(!ran)

	_ = child2.Trigger(foo, context.Background())
	assert(ran)
}
