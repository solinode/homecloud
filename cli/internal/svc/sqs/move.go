package sqs

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Message move tasks (StartMessageMoveTask) redrive messages from a
// dead-letter queue back to their source queues or to another queue,
// optionally rate limited. Tasks are kept in memory.

const (
	taskRunning    = "RUNNING"
	taskCompleted  = "COMPLETED"
	taskCancelling = "CANCELLING"
	taskCancelled  = "CANCELLED"
)

type moveTask struct {
	Handle  string
	Source  string // queue ARN
	Dest    string // queue ARN, or "" for each message's source queue
	Rate    int    // messages per second; 0 = as fast as possible
	Status  string
	Moved   int64
	ToMove  int64
	Failure string
	Started time.Time
	cancel  chan struct{}
}

type moveTasks struct {
	mu       sync.Mutex
	byHandle map[string]*moveTask
	list     []*moveTask // newest last
}

func (s *Service) startMove(source, dest string, rate int) (*moveTask, error) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	for _, t := range s.tasks.list {
		if t.Source == source && (t.Status == taskRunning || t.Status == taskCancelling) {
			return nil, core.Errf(400, "UnsupportedOperation", "There is already a task running. Only one active task is allowed for each source queue arn at a given time.")
		}
	}
	visible, _, _ := s.counts(NameFromARN(source))
	id, _ := json.Marshal(map[string]string{"taskId": uuid(), "sourceArn": source})
	t := &moveTask{Handle: base64.StdEncoding.EncodeToString(id), Source: source, Dest: dest, Rate: rate,
		Status: taskRunning, ToMove: int64(visible), Started: time.Now(), cancel: make(chan struct{})}
	s.tasks.byHandle[t.Handle] = t
	s.tasks.list = append(s.tasks.list, t)
	go s.runMove(t)
	return t, nil
}

func (s *Service) runMove(t *moveTask) {
	defer core.Recover("sqs message move task")
	src, dest := NameFromARN(t.Source), ""
	if t.Dest != "" {
		dest = NameFromARN(t.Dest)
	}
	finish := func(status string) {
		s.tasks.mu.Lock()
		t.Status = status
		s.tasks.mu.Unlock()
	}
	for {
		select {
		case <-t.cancel:
			finish(taskCancelled)
			return
		default:
		}
		s.tasks.mu.Lock()
		remaining := t.ToMove - t.Moved
		s.tasks.mu.Unlock()
		batch := int(remaining)
		if t.Rate > 0 {
			batch = min(batch, t.Rate)
		}
		n := 0
		if batch > 0 {
			n = s.moveMessages(src, dest, batch, true)
		}
		s.tasks.mu.Lock()
		t.Moved += int64(n)
		done := n == 0 || t.Moved >= t.ToMove
		s.tasks.mu.Unlock()
		if done {
			finish(taskCompleted)
			return
		}
		if t.Rate > 0 {
			select {
			case <-t.cancel:
				finish(taskCancelled)
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (s *Service) cancelMove(handle string) (*moveTask, error) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	t := s.tasks.byHandle[handle]
	if t == nil || t.Status != taskRunning {
		return nil, core.Errf(400, "ResourceNotFoundException", "Task does not exist or is not running.")
	}
	t.Status = taskCancelling
	close(t.cancel)
	return t, nil
}

// listMoves returns up to max tasks for a source queue, newest first (a copy).
func (s *Service) listMoves(source string, max int) []moveTask {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	var out []moveTask
	for i := len(s.tasks.list) - 1; i >= 0 && len(out) < max; i-- {
		if t := s.tasks.list[i]; t.Source == source {
			out = append(out, *t)
		}
	}
	return out
}
