package chord_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"syscall"
	"time"

	"github.com/sharnoff/chord"
)

// Shutdown will be used as the signal to trigger shutdown hooks
var Shutdown shutdown

type shutdown struct{}

// returns a context that is canceled after 1 second
func makeShutdownContext() context.Context {
	// it's ok to leak the context here; this is just for shutdown.
	ctx, _ := context.WithTimeout(context.TODO(), time.Second)
	return ctx
}

// Start an HTTP "hello world" server that exits on SIGTERM or SIGINT, or after 2 seconds.
func Example() {
	mgr := chord.NewSignalManager()
	defer mgr.Stop()
	// Once done, wait for shutdown to complete
	defer func() {
		if err := mgr.TryWait(Shutdown, makeShutdownContext()); err != nil {
			log.Fatalf("timed out waiting for shutdown: %s", err)
		}
	}()

	// Forward OS signals to our custom Shutdown signal
	_ = mgr.On(syscall.SIGTERM, context.TODO(), func(ctx context.Context) error {
		return mgr.TriggerAndWait(Shutdown, ctx)
	})
	_ = mgr.On(syscall.SIGINT, context.TODO(), func(ctx context.Context) error {
		return mgr.TriggerAndWait(Shutdown, ctx)
	})
	// Log that shutdown completed successfully, once done
	_ = mgr.On(Shutdown, context.TODO(), func(context.Context) error {
		fmt.Println("Shutdown complete!")
		return nil
	})

	server := &http.Server{
		Addr: ":8080",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("hello, world!"))
		}),
	}

	// Register the shutdown callback
	withHandler := mgr.WithErrorHandler(func(c context.Context, err error) error {
		if err != nil {
			log.Fatal(err)
		}
		return nil
	})
	// error will be handled; we don't need to worry about it
	_ = withHandler.On(Shutdown, context.TODO(), func(ctx context.Context) error {
		err := server.Shutdown(ctx)
		return err
	})

	// timeout after 2 seconds
	go func() {
		time.Sleep(2 * time.Second)
		// safe to ignore error because of the error handling above. Otherwise, that error could
		// appear here.
		_ = mgr.TriggerAndWait(Shutdown, makeShutdownContext())
	}()

	fmt.Println("Starting server")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	// Output:
	// Starting server
	// Shutdown complete!
}
