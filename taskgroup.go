package chord

import (
	"context"
	"fmt"
	"sync"
)

// TaskGroup provides [sync.WaitGroup]-like functionality, with the following changes:
//
//  1. Tasks are named, added one at a time with [TaskGroup.Add]
//  2. TaskGroups are hierarchical, with subgroups that can be separately Wait-ed on
//  3. [TaskGroup.Wait] returns a channel, so it can be selected over
//  4. The set of running tasks can be fetched with [TaskGroup.Tasks], [TaskGroup.Subgroups], or
//     [Taskgroup.TaskTree].
//  5. More tasks may be added after all have been completed
//
// Other than those, the general idea should be roughly familiar to users of sync.WaitGroup.
//
// See also: [TaskTree], [TaskInfo].
type TaskGroup struct {
	mu             sync.Mutex
	parent         *TaskGroup
	idInParent     subgroupID
	name           string
	count          uint
	allDone        chan struct{}
	tasks          map[string]uint
	subgroups      map[subgroupID]*TaskGroup
	nextSubgroupID subgroupID
}

// TaskTree represents the structure unfinished tasks in a [TaskGroup], returned by
// [TaskGroup.TaskTree].
type TaskTree struct {
	Name      string     `json:"name"`
	Tasks     []TaskInfo `json:"tasks"`
	Subgroups []TaskTree `json:"subgroups"`
}

type subgroupID uint64

func (g *TaskGroup) initialize() {
	if g.tasks == nil {
		g.tasks = make(map[string]uint)
		g.subgroups = make(map[subgroupID]*TaskGroup)
	}
}

// NewTaskGroup creates a new TaskGroup with the given name
func NewTaskGroup(name string) *TaskGroup {
	return &TaskGroup{name: name}
}

// Name returns the name of the TaskGroup, as constructed via [NewTaskGroup] or
// [TaskGroup.NewSubgroup].
func (g *TaskGroup) Name() string {
	return g.name
}

// NewSubgroup creates a new TaskGroup that is contained within g.
//
// Waiting on the parent TaskGroup will not complete if the child TaskGroup has unfinished tasks.
func (g *TaskGroup) NewSubgroup(name string) *TaskGroup {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initialize()

	id := g.nextSubgroupID
	g.nextSubgroupID += 1
	return &TaskGroup{
		parent:     g,
		idInParent: id,
		name:       name,
	}
}

// Add adds a task with the name to the TaskGroup. Add may be called multiple times with the same
// name, in which case multiple instances of that task will be counted.
//
// Waiting on the TaskGroup will not complete until there is exactly one call to [TaskGroup.Done]
// with a matching name for each call to Add.
func (g *TaskGroup) Add(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initialize()

	g.count += 1
	c, _ := g.tasks[name]
	g.tasks[name] = c + 1
	g.rectifyAdded()
}

func (g *TaskGroup) rectifyAdded() {
	if g.count+uint(len(g.subgroups)) == 1 && g.parent != nil {
		g.parent.mu.Lock()
		defer g.parent.mu.Unlock()

		g.parent.subgroups[g.idInParent] = g
		g.parent.rectifyAdded()
	}
}

// Done marks a task with the name as completed.
//
// Done will panic if there aren't any remaining tasks with the name.
func (g *TaskGroup) Done(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initialize()

	c, _ := g.tasks[name]
	if c == 0 {
		panic(fmt.Sprintf("zero remaining tasks with name %q", name))
	}

	c -= 1
	if c == 0 {
		delete(g.tasks, name)
	} else {
		g.tasks[name] = c
	}

	g.count -= 1
	g.rectifyDone()
}

func (g *TaskGroup) rectifyDone() {
	if g.count+uint(len(g.subgroups)) == 0 {
		if g.allDone != nil {
			close(g.allDone)
			g.allDone = nil
		}

		if g.parent != nil {
			g.parent.mu.Lock()
			defer g.parent.mu.Unlock()

			delete(g.parent.subgroups, g.idInParent)
			g.parent.rectifyDone()
		}
	}
}

// Wait returns a channel that is closed once all tasks have been completed with [TaskGroup.Done].
func (g *TaskGroup) Wait() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initialize()

	if g.count == 0 && len(g.subgroups) == 0 {
		return alwaysClosed
	}

	if g.allDone == nil {
		g.allDone = make(chan struct{})
	}

	return g.allDone
}

// TryWait Waits on the TaskGroup, returning early with ctx.Err() if the context is canceled.
//
// If the context is already canceled when TryWait is called, this method will always return the
// context's error.
func (g *TaskGroup) TryWait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-g.Wait():
			return nil
		}
	}
}

// Finished returns whether all tasks are finished, i.e. if waiting will immediately complete.
func (g *TaskGroup) Finished() bool {
	select {
	case <-g.Wait():
		return true
	default:
		return false
	}
}

// TaskInfo returns information about a set of tasks with a particular name.
//
// Instances of TaskInfo are produced by [TaskGroup.Tasks] and [TaskGroup.TaskTree].
type TaskInfo struct {
	Name string `json:"name"`
	// Count provides the number of running tasks named Name. Count is never zero when returned by
	// [TaskGroup.Tasks] or [TaskGroup.TaskTree].
	Count uint `json:"count"`
}

// Tasks returns information about the set of running tasks. It does not recurse into subgroups.
//
// Each returned TaskInfo is guaranteed to have a Count greater than zero, representing the number
// of tasks with that name. If all task names are unique, all task counts will be 1.
//
// To get information about tasks and subgroups at the same time, try [TaskGroup.TaskTree].
func (g *TaskGroup) Tasks() []TaskInfo {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.tasks == nil {
		return nil
	}

	var ts []TaskInfo
	for name, count := range g.tasks {
		ts = append(ts, TaskInfo{Name: name, Count: count})
	}
	return ts
}

// Subgroups returns the set of TaskGroups with running tasks.
//
// Note that between calling Subgroups and method on the returned TaskGroups, it may be possible
// that some or all of them have finished.
func (g *TaskGroup) Subgroups() []*TaskGroup {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.subgroups == nil {
		return nil
	}

	var sgs []*TaskGroup
	for _, sg := range g.subgroups {
		sgs = append(sgs, sg)
	}
	return sgs
}

// TaskTree returns a snapshot of all running tasks.
//
// If tasks are being added or removed during the call to TaskTree, the returned structure may not
// exactly match the state at any particular point in time - e.g. some returned subgroups may have
// no tasks.
//
// In general, any property that is true at the start of calling TaskTree and remains true through
// to when the call is finished (like "task X in subgroup Y is running") will be correctly
// represented in the returend [TaskTree]. Changes that occur during the call may be missing.
//
// The recommended use of this method is for runtime diagnostics - like having better information
// about exactly which tasks are still running, when waiting for something to stop.
func (g *TaskGroup) TaskTree() TaskTree {
	g.mu.Lock()
	locked := true
	defer func() {
		if locked {
			g.mu.Unlock()
		}
	}()

	var tasks []TaskInfo
	if g.tasks != nil {
		for name, count := range g.tasks {
			tasks = append(tasks, TaskInfo{Name: name, Count: count})
		}
	}

	var sgs []*TaskGroup
	if g.subgroups != nil {
		for _, sg := range g.subgroups {
			sgs = append(sgs, sg)
		}
	}

	// Unlock during tree traversal; otherwise we could cause deadlocks
	locked = false
	g.mu.Unlock()

	var subgroups []TaskTree
	for _, sg := range sgs {
		t := sg.TaskTree()
		if len(t.Tasks) != 0 || len(t.Subgroups) != 0 {
			subgroups = append(subgroups, t)
		}
	}

	return TaskTree{
		Name:      g.name,
		Tasks:     tasks,
		Subgroups: subgroups,
	}
}
