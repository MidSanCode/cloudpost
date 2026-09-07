package sender

import (
	"log"
	"os"
	"time"
)

// RunScheduled drains due delayed sends every 10s until Stop. On dispatch
// the stored blob is enqueued through the normal queue (retry/backoff/DKIM
// all apply) and the scheduled row is removed.
func (s *Sender) RunScheduled() {
	if s.deps.Store == nil {
		return
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	s.processScheduled()
	for {
		select {
		case <-t.C:
			s.processScheduled()
		case <-s.stop:
			return
		}
	}
}

func (s *Sender) processScheduled() {
	due, err := s.deps.Store.DueScheduled(time.Now().Unix())
	if err != nil || len(due) == 0 {
		return
	}
	for _, m := range due {
		_, path, err := s.deps.Store.GetScheduled(m.ID)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[scheduled] #%d blob lost: %v (dropping)", m.ID, err)
			s.deps.Store.DeleteScheduledRow(m.ID)
			continue
		}
		if _, err := s.Enqueue(m.From, m.Recipients, raw); err != nil {
			log.Printf("[scheduled] #%d enqueue failed: %v (will retry next tick)", m.ID, err)
			continue // keep the row; retried next tick
		}
		s.deps.Store.DeleteScheduledRow(m.ID)
		log.Printf("[scheduled] #%d dispatched from=%s to=%v", m.ID, m.From, m.Recipients)
	}
}
