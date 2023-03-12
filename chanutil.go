package chord

// unexported helpers relating to channels

var alwaysClosed = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func isClosed(c <-chan struct{}) bool {
	if c == nil {
		return false
	}

	select {
	case <-c:
		return true
	default:
		return false
	}
}
