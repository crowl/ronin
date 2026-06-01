package fsutil_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crowl/ronin/tool/fsutil"
)

func TestMutationQueue(t *testing.T) {
	t.Run("serializes mutations for same path", func(t *testing.T) {
		queue := fsutil.NewMutationQueue()
		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		secondEntered := make(chan struct{})
		firstDone := make(chan error, 1)
		secondDone := make(chan error, 1)

		go func() {
			firstDone <- queue.WithFile(context.Background(), "same.txt", func() error {
				close(firstEntered)
				<-releaseFirst
				return nil
			})
		}()
		<-firstEntered

		go func() {
			secondDone <- queue.WithFile(context.Background(), "same.txt", func() error {
				close(secondEntered)
				return nil
			})
		}()

		select {
		case <-secondEntered:
			t.Fatal("second mutation entered before first completed")
		case <-time.After(20 * time.Millisecond):
		}

		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Fatalf("first WithFile() error = %v", err)
		}

		select {
		case <-secondEntered:
		case <-time.After(time.Second):
			t.Fatal("second mutation did not enter after first completed")
		}
		if err := <-secondDone; err != nil {
			t.Fatalf("second WithFile() error = %v", err)
		}
	})

	t.Run("allows concurrent mutations for different paths", func(t *testing.T) {
		queue := fsutil.NewMutationQueue()
		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		secondEntered := make(chan struct{})
		firstDone := make(chan error, 1)
		secondDone := make(chan error, 1)

		go func() {
			firstDone <- queue.WithFile(context.Background(), "first.txt", func() error {
				close(firstEntered)
				<-releaseFirst
				return nil
			})
		}()
		<-firstEntered

		go func() {
			secondDone <- queue.WithFile(context.Background(), "second.txt", func() error {
				close(secondEntered)
				return nil
			})
		}()

		select {
		case <-secondEntered:
		case <-time.After(time.Second):
			close(releaseFirst)
			t.Fatal("second mutation did not enter while different path was locked")
		}

		if err := <-secondDone; err != nil {
			t.Fatalf("second WithFile() error = %v", err)
		}
		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Fatalf("first WithFile() error = %v", err)
		}
	})

	t.Run("returns context error while waiting", func(t *testing.T) {
		queue := fsutil.NewMutationQueue()
		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		firstDone := make(chan error, 1)

		go func() {
			firstDone <- queue.WithFile(context.Background(), "same.txt", func() error {
				close(firstEntered)
				<-releaseFirst
				return nil
			})
		}()
		<-firstEntered

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var ran atomic.Bool
		err := queue.WithFile(ctx, "same.txt", func() error {
			ran.Store(true)
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			close(releaseFirst)
			t.Fatalf("WithFile() error = %v, want context.Canceled", err)
		}
		if ran.Load() {
			close(releaseFirst)
			t.Fatal("callback ran after context cancellation")
		}

		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Fatalf("first WithFile() error = %v", err)
		}
	})

	t.Run("nil queue runs callback", func(t *testing.T) {
		var queue *fsutil.MutationQueue
		var ran atomic.Bool

		err := queue.WithFile(context.Background(), "file.txt", func() error {
			ran.Store(true)
			return nil
		})
		if err != nil {
			t.Fatalf("WithFile() error = %v", err)
		}
		if !ran.Load() {
			t.Fatal("callback did not run")
		}
	})
}
