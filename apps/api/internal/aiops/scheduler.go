package aiops

import (
	"container/heap"
	"sync"
	"time"
)

// Scheduler runs delayed commands. Replaces a whole layer (Sidekiq,
// Bull, Celery, Inngest) for the simple "fire this command at time T"
// case. Tasks live in an in-memory priority queue keyed by fire time;
// a single dispatcher goroutine wakes when the next deadline arrives
// and hands the task to the engine's command runner.
type Scheduler struct {
	mu      sync.Mutex
	heap    scheduleHeap
	cond    *sync.Cond
	stop    chan struct{}
	runner  TaskRunner
	idCount int64
	tasks   map[int64]*Task // for cancel + introspection
}

// TaskRunner is supplied by the engine — invokes the buffered command
// using the same dispatch path as a regular RESP client.
type TaskRunner func(cmd string, args []string) error

// Task is one scheduled command.
type Task struct {
	ID        int64     `json:"id"`
	FireAt    time.Time `json:"fire_at"`
	Cmd       string    `json:"cmd"`
	Args      []string  `json:"args"`
	CreatedAt time.Time `json:"created_at"`
	idx       int       // heap index
	cancelled bool
}

type scheduleHeap []*Task

func (h scheduleHeap) Len() int            { return len(h) }
func (h scheduleHeap) Less(i, j int) bool  { return h[i].FireAt.Before(h[j].FireAt) }
func (h scheduleHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].idx = i; h[j].idx = j }
func (h *scheduleHeap) Push(x any)         { *h = append(*h, x.(*Task)); (*h)[len(*h)-1].idx = len(*h) - 1 }
func (h *scheduleHeap) Pop() any           { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

// NewScheduler returns an unstarted scheduler. Call Start() once the
// engine has wired the TaskRunner.
func NewScheduler() *Scheduler {
	s := &Scheduler{
		stop:  make(chan struct{}),
		tasks: map[int64]*Task{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// SetRunner plugs in the command runner; safe to call before Start.
func (s *Scheduler) SetRunner(r TaskRunner) {
	s.mu.Lock()
	s.runner = r
	s.mu.Unlock()
}

// Start kicks off the dispatcher goroutine.
func (s *Scheduler) Start() { go s.loop() }

// Stop signals the dispatcher to exit.
func (s *Scheduler) Stop() { close(s.stop); s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() }

// At schedules cmd to fire at the given absolute time. Returns the
// task ID (for Cancel / Inspect).
func (s *Scheduler) At(at time.Time, cmd string, args []string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idCount++
	t := &Task{
		ID:        s.idCount,
		FireAt:    at,
		Cmd:       cmd,
		Args:      append([]string{}, args...),
		CreatedAt: time.Now(),
	}
	heap.Push(&s.heap, t)
	s.tasks[t.ID] = t
	s.cond.Broadcast()
	return t.ID
}

// In is a convenience for At(now+d, ...).
func (s *Scheduler) In(d time.Duration, cmd string, args []string) int64 {
	return s.At(time.Now().Add(d), cmd, args)
}

// Cancel drops a pending task. Already-fired tasks return false.
// Tasks remain in the heap with `cancelled=true` and are skipped at
// fire time — this avoids the cost of removing from the middle of a
// heap, which is rarely worth optimizing for.
func (s *Scheduler) Cancel(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false
	}
	t.cancelled = true
	delete(s.tasks, id)
	return true
}

// List returns every pending task, sorted by fire time.
func (s *Scheduler) List() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		if t.cancelled {
			continue
		}
		out = append(out, *t)
	}
	// Hand-roll a sort to avoid pulling in another import — n is small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].FireAt.After(out[j].FireAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Stats snapshots scheduler state.
type SchedulerStats struct {
	Pending int    `json:"pending"`
	Total   int64  `json:"total_scheduled"`
}

// Stats returns the snapshot.
func (s *Scheduler) Stats() SchedulerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := 0
	for _, t := range s.tasks {
		if !t.cancelled {
			pending++
		}
	}
	return SchedulerStats{Pending: pending, Total: s.idCount}
}

// loop is the dispatcher goroutine. It waits on the cond var until
// the next task is due, fires it, repeats. Avoids spin-polling.
func (s *Scheduler) loop() {
	for {
		s.mu.Lock()
		// Drop cancelled heads.
		for len(s.heap) > 0 && s.heap[0].cancelled {
			heap.Pop(&s.heap)
		}
		if len(s.heap) == 0 {
			// Wait until something arrives, with a periodic timeout
			// so Stop() is responsive without an explicit signal.
			done := make(chan struct{})
			go func() {
				select {
				case <-s.stop:
					s.mu.Lock()
					s.cond.Broadcast()
					s.mu.Unlock()
				case <-time.After(time.Hour):
				case <-done:
				}
			}()
			s.cond.Wait()
			close(done)
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			default:
			}
			continue
		}
		head := s.heap[0]
		now := time.Now()
		if head.FireAt.After(now) {
			wait := head.FireAt.Sub(now)
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-time.After(wait):
			}
			continue
		}
		// Time's up — pop and fire (outside the lock).
		heap.Pop(&s.heap)
		delete(s.tasks, head.ID)
		runner := s.runner
		s.mu.Unlock()
		if runner != nil && !head.cancelled {
			_ = runner(head.Cmd, head.Args)
		}
	}
}
