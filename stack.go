package chord

import (
	"runtime"
	"strconv"
	"sync"
)

type StackTrace struct {
	Frames []StackFrame
	Parent *StackTrace
}

// TODO - want to have some kind of "N skipped" when (a) there's lots of frames and (b) many of
// those frames are duplicates

type StackFrame struct {
	Function string
	File     string
	Line     int
}

func GetStackTrace(parent *StackTrace, skip uint) StackTrace {
	frames := getFrames(skip + 1) // skip the additional frame introduced by GetStackTrace
	return StackTrace{Frames: frames, Parent: parent}
}

func (st StackTrace) String() string {
	var buf []byte

	for {
		if len(st.Frames) == 0 {
			buf = append(buf, "<empty stack>\n"...)
		} else {
			for _, f := range st.Frames {
				var function, functionTail, file, fileLineSep, line string

				if f.Function == "" {
					function = "<unknown function>"
				} else {
					function = f.Function
					functionTail = "(...)"
				}

				if f.File == "" {
					file = "<unknown file>"
				} else {
					file = f.File
					if f.Line != 0 {
						fileLineSep = ":"
						line = strconv.Itoa(f.Line)
					}
				}

				buf = append(buf, function...)
				buf = append(buf, functionTail...)
				buf = append(buf, "\n\t"...)
				buf = append(buf, file...)
				buf = append(buf, fileLineSep...)
				buf = append(buf, line...)
				buf = append(buf, byte('\n'))
			}
		}

		if st.Parent == nil {
			break
		}

		st = *st.Parent
		continue
	}

	return string(buf)
}

var pcBufPool = sync.Pool{
	New: func() any {
		buf := make([]uintptr, 128)
		return &buf
	},
}

func getPCBuffer() *[]uintptr {
	return pcBufPool.Get().(*[]uintptr)
}

func putPCBuffer(buf *[]uintptr) {
	if len(*buf) < 1024 {
		pcBufPool.Put(buf)
	}
}

func getFrames(skip uint) []StackFrame {
	skip += 2 // skip the frame introduced by this function and runtime.Callers

	pcBuf := pcBufPool.Get().(*[]uintptr)
	defer putPCBuffer(pcBuf)
	if len(*pcBuf) == 0 {
		panic("internal error: len(*pcBuf) == 0")
	}

	// read program counters into the buffer, repeating until buffer is big enough.
	//
	// This is O(n log n), where n is the true number of program counters.
	var pc []uintptr
	for {
		n := runtime.Callers(0, *pcBuf)
		if n == 0 {
			panic("runtime.Callers(0, ...) returned zero")
		}

		if n < len(*pcBuf) {
			pc = (*pcBuf)[:n]
			break
		} else {
			*pcBuf = make([]uintptr, 2*len(*pcBuf))
		}
	}

	framesIter := runtime.CallersFrames(pc)
	var frames []StackFrame
	more := true
	for more {
		var frame runtime.Frame
		frame, more = framesIter.Next()

		if skip > 0 {
			skip -= 1
			continue
		}

		frames = append(frames, StackFrame{
			Function: frame.Function,
			File:     frame.File,
			Line:     frame.Line,
		})
	}

	return frames
}
