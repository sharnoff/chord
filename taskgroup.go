package chord

import (
	"context"
	"fmt"
	"sync"
)

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

func NewTaskGroup(name string) *TaskGroup {
	return &TaskGroup{name: name}
}

func (g *TaskGroup) Name() string {
	return g.name
}

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

var alwaysClosed = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

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

func (g *TaskGroup) Finished() bool {
	select {
	case <-g.Wait():
		return true
	default:
		return false
	}
}

type TaskInfo struct {
	Name  string
	Count uint
}

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
