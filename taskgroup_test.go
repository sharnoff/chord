package chord_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/sharnoff/chord"
	"golang.org/x/exp/slices"
)

func assert(cond bool) {
	if !cond {
		panic("assertion failed")
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestTaskGroupBasic(t *testing.T) {
	t.Parallel()

	g := chord.NewTaskGroup(t.Name())
	closed := g.Wait()
	assert(isClosed(closed))
	assert(g.Finished())
	g.Add("task-1")
	assert(isClosed(closed))
	waitCh := g.Wait()
	assert(!isClosed(waitCh))
	assert(!g.Finished())
	g.Add("task-2")
	g.Add("task-2") // intentionally add a duplicate
	tasks := g.Tasks()
	slices.SortFunc(tasks, func(t1, t2 chord.TaskInfo) bool { return t1.Name < t2.Name })
	assert(slices.Equal(tasks, []chord.TaskInfo{{Name: "task-1", Count: 1}, {Name: "task-2", Count: 2}}))
	assert(!isClosed(g.Wait()))
	g.Done("task-1")
	g.Done("task-2")
	assert(!isClosed(waitCh))
	assert(!g.Finished())
	g.Done("task-2")
	assert(isClosed(waitCh))
	assert(isClosed(g.Wait()))
	assert(g.Finished())
}

func TestTaskWaitContext(t *testing.T) {
	g := chord.NewTaskGroup(t.Name())

	tryWait := func(ctx context.Context, done chan struct{}, err *error) {
		*err = g.TryWait(ctx)
		close(done)
	}

	jiffy := time.Millisecond

	// TryWait returns nil if all tasks are done and the context hasn't been canceled
	{
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		var err error
		go tryWait(ctx, done, &err)

		time.Sleep(jiffy)
		assert(isClosed(done))
		assert(err == nil)
	}

	g.Add("task-1")

	// TryWait returns when the context is canceled
	{
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		var err error
		go tryWait(ctx, done, &err)

		time.Sleep(jiffy)
		assert(!isClosed(done))

		cancel()
		time.Sleep(jiffy)
		assert(isClosed(done))
		assert(err != nil)
	}

	// TryWait returns when all tasks finish
	{
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		var err error
		go tryWait(ctx, done, &err)

		time.Sleep(jiffy)
		assert(!isClosed(done))

		g.Done("task-1")

		time.Sleep(jiffy)
		assert(isClosed(done))
		assert(err == nil)
	}

	// calling TryWait with a canceled context always returns err, even if all tasks are done
	{
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		done := make(chan struct{})
		var err error
		go tryWait(ctx, done, &err)

		time.Sleep(jiffy)
		assert(isClosed(done))
		assert(err != nil)
	}
}

func TestTaskGroupSubgroup(t *testing.T) {
	t.Parallel()

	g := chord.NewTaskGroup(t.Name())
	g.Add("task-1")

	sg1 := g.NewSubgroup("subgroup-1")
	sg2 := g.NewSubgroup("subgroup-2")

	assert(slices.Equal(g.Tasks(), []chord.TaskInfo{{Name: "task-1", Count: 1}}))
	assert(slices.Equal(g.Subgroups(), nil)) // no tasks added for subgroups, they're not counted

	sg1.Add("sg1-task-1")
	assert(slices.Equal(g.Tasks(), []chord.TaskInfo{{Name: "task-1", Count: 1}})) // hasn't changed
	assert(slices.Equal(g.Subgroups(), []*chord.TaskGroup{sg1}))

	baseWaitCh := g.Wait()
	assert(!isClosed(baseWaitCh))
	g.Done("task-1")
	assert(!isClosed(baseWaitCh))

	sg2.Add("sg2-task-1")
	subgroups := g.Subgroups()
	slices.SortFunc(subgroups, func(s1, s2 *chord.TaskGroup) bool { return s1.Name() < s2.Name() })
	assert(slices.Equal(subgroups, []*chord.TaskGroup{sg1, sg2}))

	sg1.Done("sg1-task-1")
	assert(slices.Equal(g.Subgroups(), []*chord.TaskGroup{sg2}))

	sg2.Done("sg2-task-1")
	assert(isClosed(baseWaitCh))
	assert(g.Finished())

	sg2sg := sg2.NewSubgroup("subgroup-2-subgroup")
	sg2sg.Add("sg2sg-task-1")

	assert(!g.Finished())
	assert(slices.Equal(g.Subgroups(), []*chord.TaskGroup{sg2}))
	assert(slices.Equal(sg2.Subgroups(), []*chord.TaskGroup{sg2sg}))

	sg2sg.Done("sg2sg-task-1")
	assert(g.Finished())
	assert(g.Subgroups() == nil)
	assert(sg2.Subgroups() == nil)
}

func TestTaskGroupDoubleDonePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			panic("should have panicked")
		}
	}()

	g := chord.NewTaskGroup(t.Name())
	g.Add("task-1")
	g.Done("task-1")
	g.Done("task-1")
}

func TestTaskGroupDoneMissingPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			panic("should have panicked")
		}
	}()

	g := chord.NewTaskGroup(t.Name())
	g.Done("task-1")
}

func TestTaskGroupManyConcurrent(t *testing.T) {
	minSleepMicros := 10
	maxSleepMicros := 100
	scriptSize := 1000
	iterations := 1000
	parallelism := 100

	sleepScript := make([]time.Duration, scriptSize)
	scriptOffsets := make([]int, parallelism)

	for i := 0; i < scriptSize; i += 1 {
		sleepScript[i] = time.Microsecond * time.Duration(minSleepMicros+rand.Intn(maxSleepMicros-minSleepMicros))
	}
	for i := 0; i < parallelism; i += 1 {
		scriptOffsets[i] = rand.Intn(scriptSize)
	}

	wg := sync.WaitGroup{}
	wg.Add(parallelism)

	baseGroup := chord.NewTaskGroup(t.Name())

	for i := 0; i < parallelism; i += 1 {
		go func(i int) {
			offset := scriptOffsets[i]
			taskName := fmt.Sprintf("task-%d", i)
			subgroupName := fmt.Sprintf("subgroup-%d", i)
			subgroup := baseGroup.NewSubgroup(subgroupName)

			for iter := 0; iter < iterations; iter += 1 {
				g := baseGroup
				if (iter/4)%2 == 0 {
					g = subgroup
				}

				if (iter/2)%2 == 0 {
					g.Add(taskName)
				} else {
					g.Done(taskName)
				}

				time.Sleep(sleepScript[(iter+offset)%scriptSize])
			}

			wg.Done()
		}(i)
	}

	wg.Wait()
}
