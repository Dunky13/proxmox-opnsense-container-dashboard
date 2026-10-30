package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type State struct {
	Schema        int                   `json:"schema"`
	Demo          bool                  `json:"demo"`
	Config        Config                `json:"config"`
	Owned         map[string]Ownership  `json:"owned"`
	Plans         map[string]Plan       `json:"plans"`
	Operations    map[string]Operation  `json:"operations"`
	Jobs          map[string]Job        `json:"jobs"`
	DemoResources map[string][]Resource `json:"demoResources,omitempty"`
}
type Store struct {
	mu       sync.Mutex
	path     string
	state    State
	lock     *os.File
	poisoned bool
	// Process-local observation generation, including uncertain persistence and
	// mutation attempts. Never persisted or exposed as a settings revision.
	observationVersion uint64
}

func OpenStore(dir string, demo bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("state directory must be a real directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	for _, name := range []string{"state.lock", "state.json"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("state files must be regular files")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	lock, e := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, fmt.Errorf("state directory is already in use")
	}
	s := &Store{path: filepath.Join(dir, "state.json"), lock: lock}
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	b, e := os.ReadFile(s.path)
	if errors.Is(e, os.ErrNotExist) {
		s.state = State{1, demo, Config{1, []Host{}, []OPNsense{}}, map[string]Ownership{}, map[string]Plan{}, map[string]Operation{}, map[string]Job{}, nil}
		if e = s.persist(s.state); e != nil {
			return fail(e)
		}
		return s, nil
	}
	if e != nil {
		return fail(e)
	}
	if e = json.Unmarshal(b, &s.state); e != nil {
		return fail(fmt.Errorf("invalid durable state"))
	}
	if s.state.Schema != 1 || s.state.Demo != demo || s.state.Owned == nil || s.state.Operations == nil || s.state.Jobs == nil || s.state.Plans == nil {
		return fail(fmt.Errorf("state schema or provider mode mismatch"))
	}
	if e = ValidateConfig(s.state.Config); e != nil {
		return fail(fmt.Errorf("invalid stored configuration: %w", e))
	}
	if e = os.Chmod(s.path, 0600); e != nil {
		return fail(e)
	}
	now := time.Now().UTC()
	for id, op := range s.state.Operations {
		if op.Status == "running" {
			op.Status = "interrupted"
			op.Error = "process interrupted; inspect provider state"
			op.UpdatedAt = now
			s.state.Operations[id] = op
		}
	}
	for id, j := range s.state.Jobs {
		if j.Status == "queued" || j.Status == "running" {
			j.Status = "interrupted"
			j.Error = "process interrupted; remote outcome unknown"
			j.UpdatedAt = now
			s.state.Jobs[id] = j
		}
	}
	if e = s.persist(s.state); e != nil {
		return fail(e)
	}
	return s, nil
}
func (s *Store) Close() error {
	if s.lock != nil {
		e := s.lock.Close()
		s.lock = nil
		return e
	}
	return nil
}
func clone[T any](v T) T     { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func (s *Store) Read() State { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.state) }
func (s *Store) readObservationState() (State, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state), s.observationVersion, !s.poisoned
}
func (s *Store) invalidateObservations() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observationVersion++
}
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observationVersion++
	if s.poisoned {
		return fmt.Errorf("state persistence uncertain; restart and inspect state")
	}
	next := clone(s.state)
	if e := fn(&next); e != nil {
		return e
	}
	if e := s.persist(next); e != nil {
		s.poisoned = true
		return fmt.Errorf("state persistence failed")
	}
	s.state = next
	return nil
}
func (s *Store) persist(v State) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(name, s.path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(s.path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func decodeBounded(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return bad("invalid JSON request")
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return bad("expected one JSON object")
	}
	return nil
}
