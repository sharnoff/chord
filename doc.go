// obligatory // comment

/*
Package chord provides a collection of tools for task management, with a focus on minimizing magic.

Broadly, the tools belong to a few distinct groups:

- Signal handling: [SignalManager] and [SignalRegister]
- Stack trace collection, printing, and appending: [StackTrace], [GetStackTrace], and [StackFrame]
- Hierarchical, named sync.WaitGroup: [TaskGroup], [NewTaskGroup], [TaskTree], [TaskInfo]

For an example of using all of it together, see: <https://github.com/neondatabase/autoscaling/blob/sharnoff/chord-task-mgmt/pkg/task/manager.go>.

# Signal handling

The general idea behind signal handling via [SignalManager] is that signals are mostly user-defined,
trigger exactly once, and are hierarchical (i.e. triggering a signal in a child SignalManager does
not affect the parent).

Users of SignalManager register callbacks via [SignalManager.On]. Callbacks are called at most
once, when the signal is triggered. Callbacks are processed sequentially in the reverse order of
when they're registered, and may return error. Unhandled errors will prevent calling any remaining
callbacks.

Signals can be manually triggered with [SignalManager.Trigger]. There's special handling around
[os.Signal] values so that forwarding into the SignalManager is automatically set up when
required.

For more, see [SignalManager].

# Stack traces

The primary goal of the stack trace tooling here is to make it easy to link stack traces across
goroutines. To that end, [GetStackTrace] may be given a parent [StackTrace] to use, which gets
appended on producing a string.

The stack trace management is designed with simplicity in mind, with optimizations for collection
but not printing (i.e. GetStackTrace should be fast, but there's faster ways to print than via
[StackTrace.String]).

For more, see [StackTrace].

# Hierarchical WaitGroup

[TaskGroup] is essentially a glorified [sync.WaitGroup]. The main features it adds are: subgroups,
named Add/Done, and fetching all tasks not yet Done. It also exposes [TaskGroup.Wait] as a channel,
so you can select over it. [TaskGroup.TryWait] takes a [context.Context] and does that selection for
you.

For more, see [TaskGroup].
*/
package chord
