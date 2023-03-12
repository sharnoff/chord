package chord

// unexported helpers relating to channels

var alwaysClosed = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()
