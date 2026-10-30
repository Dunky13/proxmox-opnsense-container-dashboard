package dashboard

import (
	"context"
	"sync"
)

// readProviderRows runs only independent reads. Results retain upstream order;
// an error cancels queued/in-flight reads and all workers join before return.
// Mutations and their freshness checks must remain outside this worker pool.
func readProviderRows[T any](ctx context.Context, count int, read func(context.Context, int) (T, error)) ([]T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]T, count)
	var mu sync.Mutex
	var workers sync.WaitGroup
	next := 0
	var firstErr error
	for range min(2, count) {
		workers.Go(func() {
			for {
				mu.Lock()
				if ctx.Err() != nil || next == count {
					mu.Unlock()
					return
				}
				index := next
				next++
				mu.Unlock()
				result, err := read(ctx, index)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					cancel()
					mu.Unlock()
					return
				}
				results[index] = result
			}
		})
	}
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, unavailable("provider reads cancelled")
	}
	return results, nil
}
