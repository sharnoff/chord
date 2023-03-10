# Chord — a task managment toolkit for Go

[![Stability: Experimental](https://masterminds.github.io/stability/experimental.svg)](https://masterminds.github.io/stability/experimental.html)
[![GoDoc](https://godoc.org/github.com/sharnoff/chord?status.png)](https://pkg.go.dev/github.com/sharnoff/chord)
[![report card](https://goreportcard.com/badge/github.com/sharnoff/chord)](https://goreportcard.com/report/github.com/sharnoff/chord)

This package provides a collection of tools for building your own internal task management.
The needs of different projects differ, so an opinionated one-stop-solution will necessarily have
limited use. This package aims to provide the building blocks that tend to be common across such
systems.

For an example of this package in use, see: <https://github.com/neondatabase/autoscaling/blob/sharnoff/chord-task-mgmt/pkg/task/manager.go>.

## Feature summary

Broadly there's three categories of things provided by this package:

* Stack trace chaining — for including callers in the stack traces of goroutines
* Signal handling — both OS signals and user-defined signals, like "shutdown"
* Hierarchical, named `sync.WaitGroup` — for tracking tasks: getting the active set and waiting on a subset to finish

For full API documentation, refer to godoc: <https://godoc.org/github.com/sharnoff/chord>.
