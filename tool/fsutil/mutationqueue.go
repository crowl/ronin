package fsutil

import (
	"context"
	"path/filepath"
	"sync"
)

// MutationQueue serializes file mutations per resolved path. It is safe for
// concurrent use; a nil queue runs callbacks without coordination.
type MutationQueue struct {
	mu     sync.Mutex
	queues map[string]*mutationLock
}

// mutationLock is a one-slot semaphore so waiters can block on acquisition
// while still observing context cancellation.
type mutationLock struct {
	slot chan struct{}
	refs int
}

func NewMutationQueue() *MutationQueue {
	return &MutationQueue{
		queues: make(map[string]*mutationLock),
	}
}

func (q *MutationQueue) WithFile(ctx context.Context, path string, fn func() error) error {
	return q.withKey(ctx, mutationFileKey(path), fn)
}

func (q *MutationQueue) WithPath(ctx context.Context, path string, fn func() error) error {
	key, err := mutationPathKey(path)
	if err != nil {
		return err
	}
	return q.withKey(ctx, key, fn)
}

func (q *MutationQueue) withKey(ctx context.Context, key string, fn func() error) error {
	if q == nil {
		return fn()
	}

	lock := q.getLock(key)
	defer q.releaseLockRef(key, lock)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	select {
	case lock.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lock.slot }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return fn()
}

func (q *MutationQueue) getLock(key string) *mutationLock {
	q.mu.Lock()
	defer q.mu.Unlock()

	lock := q.queues[key]
	if lock == nil {
		lock = &mutationLock{slot: make(chan struct{}, 1)}
		q.queues[key] = lock
	}
	lock.refs++
	return lock
}

func (q *MutationQueue) releaseLockRef(key string, lock *mutationLock) {
	q.mu.Lock()
	defer q.mu.Unlock()

	lock.refs--
	if lock.refs == 0 && q.queues[key] == lock {
		delete(q.queues, key)
	}
}

func mutationFileKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if realPath, err := filepath.EvalSymlinks(abs); err == nil {
		return realPath
	}
	if resolvedPath, err := ResolveExistingOrParent(abs); err == nil {
		return resolvedPath
	}
	return abs
}

func mutationPathKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := resolveParent(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
