package worldradar

import (
	"context"
	"encoding/json"
	"time"
)

const defaultSchedulerTick = 5 * time.Minute

// StartScheduler starts the background polling loop for active instances whose config_json has "autoPoll":true.
func (e *Engine) StartScheduler(tickInterval time.Duration) {
	if tickInterval <= 0 {
		tickInterval = defaultSchedulerTick
	}
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.closed || e.schedStarted || e.schedCtx == nil || e.schedCtx.Err() != nil {
		return
	}
	e.schedStarted = true
	e.schedWG.Add(1)
	go e.runSchedulerLoop(tickInterval)
}

func (e *Engine) runSchedulerLoop(tickInterval time.Duration) {
	defer e.schedWG.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.schedCtx.Done():
			return
		case <-ticker.C:
			e.pollAutoInstances(e.schedCtx)
		}
	}
}

func (e *Engine) pollAutoInstances(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	instances, err := e.ListInstances(ctx)
	if err != nil {
		return
	}
	for _, inst := range instances {
		if ctx.Err() != nil {
			return
		}
		if inst.Status != "active" {
			continue
		}
		var cfg struct {
			AutoPoll bool `json:"autoPoll"`
		}
		if err := json.Unmarshal([]byte(inst.ConfigJSON), &cfg); err != nil || !cfg.AutoPoll {
			continue
		}
		_, _ = e.RunInstance(ctx, inst.ID)
	}
}

// Close cancels the scheduler context and waits for the background loop to exit cleanly.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.schedMu.Lock()
		e.closed = true
		if e.schedCancel != nil {
			e.schedCancel()
		}
		e.schedMu.Unlock()
		e.schedWG.Wait()
	})
	return nil
}
