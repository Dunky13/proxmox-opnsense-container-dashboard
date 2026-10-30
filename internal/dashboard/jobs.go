package dashboard

import (
	"context"
	"errors"
	"sort"
	"time"
)

type JobRequest struct {
	HostID      string `json:"hostId"`
	VMID        int    `json:"vmid"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

func jobBlocks(j Job) bool {
	return j.Status == "queued" || j.Status == "running" || ((j.Status == "timedOut" || j.Status == "interrupted" || j.Status == "rebootRequired") && !j.Acknowledged)
}
func (s *Service) StartJob(ctx context.Context, req JobRequest) (Job, error) {
	if !s.mutate.TryLock() {
		return Job{}, conflict("another mutation is active")
	}
	defer s.mutate.Unlock()
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	s.Store.invalidateObservations()
	defer s.Store.invalidateObservations()
	if req.Kind != "appUpdate" && req.Kind != "osUpdate" {
		return Job{}, bad("unsupported update kind")
	}
	if req.VMID < 100 || req.VMID > 999999999 {
		return Job{}, bad("invalid VMID")
	}
	st := s.Store.Read()
	h, ok := st.Config.Host(req.HostID)
	if !ok {
		return Job{}, bad("unknown host")
	}
	if !h.SSH.Enabled && !st.Demo {
		return Job{}, conflict("SSH must be enabled")
	}
	if routerBlocked(st, h.OPNsenseID) {
		return Job{}, conflict("linked firewall has an unfinished operation or update job")
	}
	count := 0
	for _, j := range st.Jobs {
		if j.Status == "queued" || j.Status == "running" {
			count++
		}
		if j.Key == key(req.HostID, req.VMID) && jobBlocks(j) {
			return Job{}, conflict("container has an active or unacknowledged uncertain job")
		}
	}
	if count >= 32 {
		return Job{}, conflict("job queue is full")
	}
	inv := s.Providers.Inventory(ctx, st.Config)
	c, e := ensureContainer(inv, h, req.VMID)
	if e != nil {
		return Job{}, e
	}
	if c.Status != "running" {
		return Job{}, conflict("updates require a running container")
	}
	if req.Fingerprint == "" || req.Fingerprint != c.Fingerprint {
		return Job{}, conflict("container changed; refresh inventory")
	}
	if e = s.freshJobCapability(ctx, h, req.VMID, req.Kind); e != nil {
		return Job{}, e
	}
	now := time.Now().UTC()
	j := Job{ID: newID(), Key: c.Key, HostID: h.ID, VMID: c.VMID, Kind: req.Kind, Fingerprint: c.Fingerprint, Status: "queued", CreatedAt: now, UpdatedAt: now, Logs: []string{"Update requested explicitly; queued."}}
	e = s.Store.Update(func(st *State) error {
		if len(st.Jobs) >= 200 {
			oldest := ""
			var at time.Time
			for id, job := range st.Jobs {
				if !jobBlocks(job) && (oldest == "" || job.CreatedAt.Before(at)) {
					oldest = id
					at = job.CreatedAt
				}
			}
			if oldest == "" {
				return conflict("job history is full of unresolved jobs")
			}
			delete(st.Jobs, oldest)
		}
		st.Jobs[j.ID] = j
		return nil
	})
	if e != nil {
		return j, e
	}
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.runJob(j, h, c.Identity) }()
	return j, nil
}
func (s *Service) saveJob(j *Job, message string) error {
	j.UpdatedAt = time.Now().UTC()
	if message != "" {
		if len(j.Logs) < 100 {
			j.Logs = append(j.Logs, message)
		} else {
			j.Truncated = true
		}
	}
	return s.Store.Update(func(st *State) error { st.Jobs[j.ID] = *j; return nil })
}
func (s *Service) runJob(j Job, h Host, identity string) {
	s.Store.invalidateObservations()
	defer s.Store.invalidateObservations()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-s.ctx.Done():
		j.Status = "interrupted"
		j.Error = "server stopped before execution"
		_ = s.saveJob(&j, "Execution interrupted.")
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.JobTimeout)
	defer cancel()
	j.Status = "running"
	if s.saveJob(&j, "Rechecking live container identity and running state.") != nil {
		return
	}
	inv := s.Providers.Inventory(ctx, s.Store.Read().Config)
	c, e := ensureContainer(inv, h, j.VMID)
	if e == nil && (c.Status != "running" || c.Fingerprint != j.Fingerprint || c.Identity != identity) {
		e = conflict("container changed before execution")
	}
	executed := false
	result := RunResult{}
	if e == nil {
		e = s.freshJobCapability(ctx, h, j.VMID, j.Kind)
	}

	if e == nil {
		latest := s.Providers.Inventory(ctx, s.Store.Read().Config)
		fresh, e2 := ensureContainer(latest, h, j.VMID)
		if e2 != nil {
			e = e2
		} else if fresh.Status != "running" || fresh.Identity != identity || fresh.Fingerprint != j.Fingerprint {
			e = conflict("container changed before command execution")
		}
	}
	if e == nil {
		if e = s.saveJob(&j, "Executing fixed update command; remote output is withheld to protect secrets."); e == nil {
			executed = true
			result, e = s.Providers.Run(ctx, h, j.VMID, j.Kind)
		}
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		j.Status = "timedOut"
		j.Error = "timeout; remote command may still be running; inspect before acknowledging"
	case s.ctx.Err() != nil:
		j.Status = "interrupted"
		j.Error = "server shutdown; remote outcome is unknown"
	case e != nil && executed:
		j.Status = "interrupted"
		j.Error = "SSH update did not complete; inspect container before acknowledging"
	case e != nil:
		j.Status = "failed"
		j.Error = "preflight failed; verify current inventory, SSH and supported distribution"
	case result.RebootRequired:
		j.Status = "rebootRequired"
		j.Error = "update completed; container reports reboot required; no reboot was issued"
	default:
		j.Status = "succeeded"
	}
	_ = s.saveJob(&j, "Job ended: "+j.Status+".")
}
func (s *Service) Jobs() []Job {
	out := []Job{}
	for _, j := range s.Store.Read().Jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (s *Service) Job(id string) (Job, error) {
	j, ok := s.Store.Read().Jobs[id]
	if !ok {
		return j, &Fault{"notFound", "job not found", 404}
	}
	return j, nil
}
func (s *Service) AcknowledgeJob(id string) (Job, error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	j, e := s.Job(id)
	if e != nil {
		return j, e
	}
	if j.Status != "timedOut" && j.Status != "interrupted" && j.Status != "rebootRequired" {
		return j, conflict("only uncertain terminal jobs need acknowledgement")
	}
	j.Acknowledged = true
	return j, s.saveJob(&j, "Operator acknowledged remote outcome; no command was repeated.")
}
