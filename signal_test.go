package chord_test

import (
	"testing"

	"github.com/sharnoff/chord"
	"golang.org/x/exp/slices"
)

func TestSignalCallbackOrdering(t *testing.T) {
	var history []int
	record := func(x int) func() {
		return func() {
			history = append(history, x)
		}
	}

	sig := "signal"
	mgr := chord.NewSignalManager()
	defer mgr.Stop()

	mgr.On(sig, record(1))

	child1 := mgr.NewChild()
	defer child1.Stop()
	child1.On(sig, record(2))

	mgr.On(sig, record(4))

	child1.On(sig, record(3))

	child2 := mgr.NewChild()
	defer child2.Stop()
	child2.On(sig, record(5))

	mgr.On(sig, record(6))

	mgr.Trigger(sig)

	if !slices.Equal(history, []int{6, 5, 4, 3, 2, 1}) {
		t.Fatalf("bad ordering, got: %v", history)
	}
}
