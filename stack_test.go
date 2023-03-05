package chord_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sharnoff/chord"
)

func concatLines(lines ...string) string {
	return strings.Join(lines, "\n")
}

func TestStackFormatVarieties(t *testing.T) {
	t.Parallel()

	expected := concatLines(
		"packagename.foo(...)",
		"\t/path/to/package/foo.go:37",
		"packagename.bar(...)",
		"\t/path/to/package/bar.go",
		"packagename.baz(...)",
		"\t<unknown file>",
		"packagename.qux(...)",
		"\t<unknown file>",
		"<unknown function>",
		"\t/unknown/function/path.go:45",
		"<unknown function>",
		"\t<unknown file>",
		"",
	)

	st := chord.StackTrace{
		Frames: []chord.StackFrame{
			{Function: "packagename.foo", File: "/path/to/package/foo.go", Line: 37},
			{Function: "packagename.bar", File: "/path/to/package/bar.go"},
			{Function: "packagename.baz"},
			{Function: "packagename.qux", Line: 29}, // Line should have no effect if File is missing.
			{File: "/unknown/function/path.go", Line: 45},
			{},
		},
	}

	got := st.String()

	if got != expected {
		t.Fail()
		t.Log(
			"--- BEGIN expected formatting ---\n",
			fmt.Sprintf("%q", expected),
			"\n--- END expected formatting. BEGIN actual formatting ---\n",
			fmt.Sprintf("%q", got),
		)
	}
}

func TestStackParentsFormat(t *testing.T) {
	t.Parallel()

	expected := concatLines(
		"packagename.Foo(...)",
		"\t/path/to/package/foo.go:37",
		"packagename.Bar(...)",
		"\t/path/to/package/bar.go:45",
		"packagename2.Baz(...)",
		"\t/path/to/package2/baz.go:52",
		"packagename2.Qux(...)",
		"\t/path/to/package2/qux.go:59",
		"packagename3.Abc(...)",
		"\t/path/to/package3/abc.go:66",
		"packagename3.Xyz(...)",
		"\t/path/to/package3/xyz.go:71",
		"",
	)

	st := chord.StackTrace{
		Frames: []chord.StackFrame{
			{Function: "packagename.Foo", File: "/path/to/package/foo.go", Line: 37},
			{Function: "packagename.Bar", File: "/path/to/package/bar.go", Line: 45},
		},
		Parent: &chord.StackTrace{
			Frames: []chord.StackFrame{
				{Function: "packagename2.Baz", File: "/path/to/package2/baz.go", Line: 52},
				{Function: "packagename2.Qux", File: "/path/to/package2/qux.go", Line: 59},
			},
			Parent: &chord.StackTrace{
				Frames: []chord.StackFrame{
					{Function: "packagename3.Abc", File: "/path/to/package3/abc.go", Line: 66},
					{Function: "packagename3.Xyz", File: "/path/to/package3/xyz.go", Line: 71},
				},
			},
		},
	}

	got := st.String()

	if got != expected {
		t.Fail()
		t.Log(
			"--- BEGIN expected formatting ---\n",
			fmt.Sprintf("%q", expected),
			"\n--- END expected formatting. BEGIN actual formatting ---\n",
			fmt.Sprintf("%q", got),
		)
	}
}

func validateStackTrace(t *testing.T, expected, got chord.StackTrace) {
	for depth := 0; ; depth += 1 {
		if (expected.Parent == nil) != (got.Parent == nil) {
			t.Fatalf(
				"mismatched at depth %d, whether has parent: expected %v, got %v",
				depth, expected.Parent != nil, got.Parent != nil,
			)
		}

		if len(expected.Frames) > len(got.Frames) || expected.Parent != nil && len(expected.Frames) != len(got.Frames) {
			t.Fatalf(
				"mismatched at depth %d, number of frames: expected %d, got %d",
				depth, len(expected.Frames), len(got.Frames),
			)
		}

		for i := range expected.Frames {
			e := expected.Frames[i]
			g := got.Frames[i]

			// check .File
			if matched, err := regexp.Match(fmt.Sprint("^", e.File, "$"), []byte(g.File)); !matched || err != nil {
				if err != nil {
					panic(fmt.Errorf("bad regex for expected at depth %d, Frames[%d].Function: %w", depth, i, err))
				}

				t.Fatalf("mismatched at depth %d, Frames[%d].File: expected match for %q, got %q", depth, i, e.File, g.File)
			}

			// check .Function
			if matched, err := regexp.Match(fmt.Sprint("^", e.Function, "$"), []byte(g.Function)); !matched || err != nil {
				if err != nil {
					panic(fmt.Errorf("bad regex for expected at depth %d, Frames[%d].Function: %w", depth, i, err))
				}

				t.Fatalf("mismatched at depth %d, Frames[%d].Function: expected match for %q, got %q", depth, i, e.Function, g.Function)
			}

			// check .Line
			if (e.Line == 0) != (g.Line == 0) {
				expectedKind := "!= 0"
				if e.Line == 0 {
					expectedKind = "== 0"
				}
				t.Fatalf("mismatched at depth %d, Frames[%d].Line: expected %s, got %d", depth, i, expectedKind, g.Line)
			}
		}

		if expected.Parent == nil {
			return
		}

		expected = *expected.Parent
		got = *got.Parent
	}
}

func TestStackBasicCreation(t *testing.T) {
	t.Parallel()

	expected := chord.StackTrace{
		Frames: []chord.StackFrame{
			{Function: `.*/chord_test.TestStackBasicCreation.func1`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*/chord_test.TestStackBasicCreation.func2`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*/chord_test.TestStackBasicCreation.func3`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*/chord_test.TestStackBasicCreation`, File: `.*/stack_test\.go`, Line: 1},
		},
	}

	func1 := func() chord.StackTrace {
		return chord.GetStackTrace(nil, 0)
	}
	func2 := func() chord.StackTrace {
		return func1()
	}
	func3 := func() chord.StackTrace {
		return func2()
	}

	got := func3()

	validateStackTrace(t, expected, got)
}

func TestStackPartialSkip(t *testing.T) {
	t.Parallel()

	expected := chord.StackTrace{
		Frames: []chord.StackFrame{
			{Function: `.*/chord_test.TestStackPartialSkip.func3`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*/chord_test.TestStackPartialSkip.func4`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*/chord_test.TestStackPartialSkip`, File: `.*/stack_test\.go`, Line: 1},
		},
	}

	func1 := func() chord.StackTrace {
		return chord.GetStackTrace(nil, 2)
	}
	func2 := func() chord.StackTrace {
		return func1()
	}
	func3 := func() chord.StackTrace {
		return func2()
	}
	func4 := func() chord.StackTrace {
		return func3()
	}

	got := func4()

	validateStackTrace(t, expected, got)
}

func TestStackSkipTooManyIsEmpty(t *testing.T) {
	t.Parallel()

	st := chord.GetStackTrace(nil, 100000) // pick a big number to skip all frames
	if len(st.Frames) != 0 {
		t.Fatal("expected no frames, got", len(st.Frames))
	}
}

func TestStackMultiCreation(t *testing.T) {
	t.Parallel()

	expected := chord.StackTrace{
		Frames: []chord.StackFrame{
			{Function: `.*/chord_test.TestStackMultiCreation.func3`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*/chord_test.TestStackMultiCreation.func1`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `runtime\.goexit`, File: `.*`, Line: 1}, // TODO: this may be fragile
		},
		Parent: &chord.StackTrace{
			Frames: []chord.StackFrame{
				{Function: `.*/chord_test.TestStackMultiCreation.func4`, File: `.*/stack_test\.go`, Line: 1},
				{Function: `.*/chord_test.TestStackMultiCreation.func5`, File: `.*/stack_test\.go`, Line: 1},
				{Function: `.*/chord_test.TestStackMultiCreation.func1`, File: `.*/stack_test\.go`, Line: 1},
				{Function: `runtime\.goexit`, File: `.*`, Line: 1},
			},
			Parent: &chord.StackTrace{
				Frames: []chord.StackFrame{
					{Function: `.*/chord_test.TestStackMultiCreation.func6`, File: `.*/stack_test\.go`, Line: 1},
					{Function: `.*/chord_test.TestStackMultiCreation.func7`, File: `.*/stack_test\.go`, Line: 1},
					{Function: `.*/chord_test.TestStackMultiCreation`, File: `.*/stack_test\.go`, Line: 1},
				},
			},
		},
	}

	send := func(ch chan chord.StackTrace, parent chord.StackTrace, f func(chord.StackTrace) chord.StackTrace) {
		ch <- f(parent)
	}

	spawnWithStack := func(p *chord.StackTrace, f func(chord.StackTrace) chord.StackTrace) chord.StackTrace {
		parent := chord.GetStackTrace(p, 1) // skip this function and the inner go func

		ch := make(chan chord.StackTrace)
		go send(ch, parent, f)
		return <-ch
	}

	func3 := func(parent chord.StackTrace) chord.StackTrace {
		return chord.GetStackTrace(&parent, 0)
	}
	func4 := func(parent chord.StackTrace) chord.StackTrace {
		return spawnWithStack(&parent, func3)
	}
	func5 := func(parent chord.StackTrace) chord.StackTrace {
		return func4(parent)
	}
	func6 := func() chord.StackTrace {
		return spawnWithStack(nil, func5)
	}
	func7 := func() chord.StackTrace {
		return func6()
	}

	got := func7()

	validateStackTrace(t, expected, got)
}

func TestStackCreateAfterRecover(t *testing.T) {
	t.Parallel()

	expected := chord.StackTrace{
		Frames: []chord.StackFrame{
			{Function: `.*chord_test.TestStackCreateAfterRecover.func1`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*chord_test.TestStackCreateAfterRecover.func2`, File: `.*/stack_test\.go`, Line: 1},
			{Function: `.*chord_test.TestStackCreateAfterRecover.func3`, File: `.*/stack_test\.go`, Line: 1},
		},
	}

	func1 := func() {
		panic("")
	}

	func2 := func() {
		func1()
	}

	var func4 func()

	func3 := func() {
		defer func4()
		func2()
	}

	var stack chord.StackTrace
	func4 = func() {
		if recover() != nil {
			stack = chord.GetStackTrace(nil, 2)
		}
	}

	func3()
	got := stack

	validateStackTrace(t, expected, got)
}
