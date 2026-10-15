package ops

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAStopAfterTheStepCutsAWaitShort(t *testing.T) {
	stop := make(chan struct{})
	waiting, done := until(context.Background(), stop)
	defer done()
	if closed(stop) || waiting.Err() != nil {
		t.Fatal("nothing asked to stop yet")
	}
	close(stop)
	select {
	case <-waiting.Done():
	case <-time.After(time.Second):
		t.Fatal("a stop must end the wait")
	}
	if !closed(stop) || !errors.Is(context.Cause(waiting), errStopped) {
		t.Fatalf("got %v", context.Cause(waiting))
	}
}

func TestAWaitEndedAnotherWayIsNoStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	waiting, done := until(ctx, nil)
	defer done()
	cancel()
	<-waiting.Done()
	if errors.Is(context.Cause(waiting), errStopped) || closed(nil) {
		t.Fatal("ctrl+c is not a stop after the step")
	}
}
