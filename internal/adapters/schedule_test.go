package adapters

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func withSpacing(t *testing.T, d time.Duration) {
	t.Helper()
	HostSpacing = d
	t.Cleanup(func() {
		HostSpacing = 0
		turns.Lock()
		turns.hosts = map[string]*hostTurns{}
		turns.Unlock()
	})
}

func TestTurnsAreSpacedPerInstance(t *testing.T) {
	withSpacing(t, 60*time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	var mu sync.Mutex
	var got []time.Duration
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := waitTurn(ctx, "a.example"); err != nil {
				t.Error(err)
			}
			mu.Lock()
			got = append(got, time.Since(start))
			mu.Unlock()
		}()
	}
	// Another instance doesn't wait for a.example.
	if err := waitTurn(ctx, "b.example"); err != nil || time.Since(start) > 30*time.Millisecond {
		t.Errorf("b.example waited: %v", time.Since(start))
	}
	wg.Wait()
	if len(got) != 3 || got[2] < 110*time.Millisecond {
		t.Errorf("three requests to one instance done after %v; want them 60ms apart", got)
	}
}

func TestNormalRequestsGoFirst(t *testing.T) {
	withSpacing(t, 80*time.Millisecond)
	ctx := context.Background()
	if err := waitTurn(ctx, "a.example"); err != nil { // takes the current turn
		t.Fatal(err)
	}
	order := make(chan string, 2)
	go func() {
		waitTurn(Background(ctx), "a.example")
		order <- "background"
	}()
	time.Sleep(10 * time.Millisecond) // the background request queues first
	go func() {
		waitTurn(ctx, "a.example")
		order <- "normal"
	}()
	if first := <-order; first != "normal" {
		t.Errorf("%s went first", first)
	}
	<-order
}

func TestTooLongAWaitGivesUp(t *testing.T) {
	withSpacing(t, time.Second)
	if err := waitTurn(context.Background(), "a.example"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // < 1s wait + requestMargin
	defer cancel()
	start := time.Now()
	err := waitTurn(ctx, "a.example")
	var pe *PausedError
	if !errors.As(err, &pe) || !pe.Busy || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("expected an immediate Busy error, got %v after %v", err, time.Since(start))
	}
	if limited, _ := RateLimit(err); !limited || !NothingSent(err) {
		t.Error("a busy queue should read as a rate limit with nothing sent")
	}

	// A request that's cancelled while waiting leaves the queue.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- waitTurn(ctx2, "a.example") }()
	time.Sleep(20 * time.Millisecond)
	cancel2()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled wait: %v", err)
	}
	turns.Lock()
	n := len(turns.hosts["a.example"].waiting)
	turns.Unlock()
	if n != 0 {
		t.Errorf("%d waiters left", n)
	}
}
