package dashboard

import (
	"context"
	"errors"
	"sync"
	"time"
)

const displayObservationTTL = 15 * time.Second
const displayObservationTimeout = 90 * time.Second

var errObservationChanged = errors.New("display observation state changed")

// Only authenticated GET display reads use these caches. The provider interface
// and all preflight, plan, apply, recovery and job reads remain fresh.
// Each cache owns at most one worker, including while an invalidated read joins.
// A disconnected waiter does not own that worker or its HTTP transport scope.
type observationCache[T any] struct {
	mu      sync.Mutex
	workers sync.WaitGroup
	closed  bool
	valid   bool
	version uint64
	expires time.Time
	value   T
	flight  *observationFlight[T]
}

type observationFlight[T any] struct {
	version uint64
	done    chan struct{}
	cancel  context.CancelFunc
	value   T
	err     error
}

func (c *observationCache[T]) read(ctx, lifetime context.Context, version uint64, load func(context.Context) (T, time.Time, bool)) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		if c.closed || lifetime.Err() != nil {
			c.mu.Unlock()
			return zero, context.Canceled
		}
		if c.version > version {
			c.mu.Unlock()
			return zero, errObservationChanged
		}
		if c.valid && c.version == version && time.Now().Before(c.expires) {
			value := clone(c.value)
			c.mu.Unlock()
			return value, nil
		}
		flight := c.flight
		if flight != nil && flight.version != version {
			if flight.version > version {
				c.mu.Unlock()
				return zero, errObservationChanged
			}
			flight.cancel()
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-lifetime.Done():
				return zero, lifetime.Err()
			case <-flight.done:
				continue
			}
		}
		if flight == nil {
			c.valid = false
			c.version = version
			work, cancel := context.WithTimeout(lifetime, displayObservationTimeout)
			flight = &observationFlight[T]{version: version, done: make(chan struct{}), cancel: cancel}
			c.flight = flight
			started := time.Now()
			c.workers.Add(1)
			go func() {
				defer c.workers.Done()
				work, closeHTTP := withProviderHTTP(work)
				value, observedAt, reusable := load(work)
				err := work.Err()
				closeHTTP()
				cancel()
				expires := started.Add(displayObservationTTL)
				if !observedAt.IsZero() && observedAt.Add(displayObservationTTL).Before(expires) {
					expires = observedAt.Add(displayObservationTTL)
				}
				c.mu.Lock()
				flight.value, flight.err = clone(value), err
				if err == nil && reusable && time.Now().Before(expires) && !c.closed {
					// Neither the loader nor a response recipient owns cache memory.
					c.value = flight.value
					c.expires, c.valid = expires, true
				}
				c.flight = nil
				close(flight.done)
				c.mu.Unlock()
			}()
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-lifetime.Done():
			return zero, lifetime.Err()
		case <-flight.done:
			if err := ctx.Err(); err != nil {
				return zero, err
			}
			return clone(flight.value), flight.err
		}
	}
}

func (c *observationCache[T]) close() {
	c.mu.Lock()
	c.closed, c.valid = true, false
	var zero T
	c.value = zero
	if c.flight != nil {
		c.flight.cancel()
	}
	c.mu.Unlock()
	c.workers.Wait()
}

func unavailableDisplayInventory(cfg Config) Inventory {
	inv := Inventory{Containers: []Container{}, Hosts: []HostInventory{}}
	for _, h := range cfg.Hosts {
		inv.Hosts = append(inv.Hosts, HostInventory{HostID: h.ID, Error: "display observation unavailable; retry refresh or inspect server state", Guests: []Guest{}})
	}
	return inv
}

func (s *Service) displayObservation(ctx context.Context, withRouters bool) (State, Inventory, routerObservations) {
	for {
		st, version, certain := s.Store.readObservationState()
		routers := routerObservations{map[string]ProviderSnapshot{}, map[string]bool{}}
		if ctx.Err() != nil || s.ctx.Err() != nil || !certain {
			return st, unavailableDisplayInventory(st.Config), routers
		}
		inv, err := s.displayInventory.read(ctx, s.ctx, version, func(work context.Context) (Inventory, time.Time, bool) {
			inv := s.Providers.Inventory(work, st.Config)
			reusable := inv.Complete && !inv.ObservedAt.IsZero() && !inv.ObservedAt.After(time.Now()) && len(inv.Hosts) == len(st.Config.Hosts)
			for _, host := range inv.Hosts {
				reusable = reusable && host.Complete && host.Error == ""
			}
			return inv, inv.ObservedAt, reusable
		})
		if err == nil && withRouters {
			routers, err = s.displayRouters.read(ctx, s.ctx, version, func(work context.Context) (routerObservations, time.Time, bool) {
				observations := s.observeRouters(work, st.Config)
				reusable := true
				for _, h := range st.Config.Hosts {
					snap := observations.Snapshots[h.ID]
					reusable = reusable && !observations.Failures[h.ID] && snap.Ready && len(snap.Issues) == 0
				}
				return observations, time.Time{}, reusable
			})
		}
		current, currentVersion, certain := s.Store.readObservationState()
		if !certain {
			return current, unavailableDisplayInventory(current.Config), routerObservations{}
		}
		if currentVersion != version || errors.Is(err, errObservationChanged) {
			// Re-read after config/credentials, ownership, activity or a mutation
			// attempt changed. Old in-flight reads cannot restore display authority.
			continue
		}
		if err != nil {
			return current, unavailableDisplayInventory(current.Config), routerObservations{}
		}
		// Ownership and operation guards are derived now, never cached in results.
		return current, inv, routers
	}
}
