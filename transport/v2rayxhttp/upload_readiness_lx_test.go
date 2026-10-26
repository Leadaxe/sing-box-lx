package v2rayxhttp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// lx: SPEC 077: a successful download header cannot substitute upload adoption.
func TestDownloadReadyDoesNotCompleteUploadRaise(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adopted, created := make(chan struct{}), make(chan struct{})
	close(created)
	result := make(chan error, 1)
	go func() { result <- awaitRaise(ctx, adopted, created, nil, func() error { return nil }) }()
	select {
	case err := <-result:
		t.Fatalf("dial completed before upload adoption: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(adopted)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("upload adoption did not complete dial")
	}
}

func TestDownloadReadyCannotHideCancelledUploadRaise(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adopted, created := make(chan struct{}), make(chan struct{})
	close(created)
	if err := awaitRaise(ctx, adopted, created, nil, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("unraised upload must respect dial cancellation: %v", err)
	}
}
