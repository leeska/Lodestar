package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/leeska/Lodestar/internal/action"
)

// runLimit caps one background turn: the gateway rounds, the tools they call
// and a confirmation's follow-up all have to finish inside it.
const runLimit = 3 * time.Minute

// liveEvent is one event of a running turn, already encoded.
type liveEvent struct {
	name string
	data []byte
}

// liveRun is a turn that runs in the background, detached from the browser
// request that started it: closing the page or switching to another page only
// drops the stream, the turn keeps going and saves its result. Any page of the
// same owner can attach again and receives every event from the start.
type liveRun struct {
	owner  string
	after  int64
	cancel context.CancelFunc

	mu      sync.Mutex
	events  []liveEvent
	done    bool
	changed chan struct{}
}

func (run *liveRun) emit(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.events = append(run.events, liveEvent{name, raw})
	close(run.changed)
	run.changed = make(chan struct{})
	return nil
}

func (run *liveRun) finish() {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.done = true
	close(run.changed)
	run.changed = make(chan struct{})
}

// next returns the events from index on, whether the run has ended, and a
// channel that is closed when more arrive.
func (run *liveRun) next(index int) ([]liveEvent, bool, <-chan struct{}) {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.events[index:], run.done, run.changed
}

// start runs work for conversation id in the background. after is the last
// message id saved before the turn: a page that attaches later shows the saved
// messages up to it and replays the rest from the events.
func (m *Module) start(ctx context.Context, id, owner string, work func(context.Context, Emit) error) (*liveRun, error) {
	var after int64
	if err := m.DB.QueryRowContext(ctx, `SELECT coalesce(max(id),0) FROM assistant_messages WHERE conversation_id=?`, id).Scan(&after); err != nil {
		return nil, err
	}
	background, cancel := context.WithTimeout(context.WithoutCancel(ctx), runLimit)
	run := &liveRun{owner: owner, after: after, cancel: cancel, changed: make(chan struct{})}
	m.mu.Lock()
	if m.runs == nil {
		m.runs = map[string]*liveRun{}
	}
	if m.runs[id] != nil {
		m.mu.Unlock()
		cancel()
		return nil, &action.Error{Status: 409, Message: "这个会话正在处理，请稍后再试"}
	}
	m.runs[id] = run
	m.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			if m.runs[id] == run {
				delete(m.runs, id)
			}
			m.mu.Unlock()
			run.finish()
		}()
		if err := work(background, run.emit); err != nil {
			run.emit("error", map[string]string{"error": safeError(err)})
		}
	}()
	return run, nil
}

// live is the running turn of conversation id that owner may see.
func (m *Module) live(id, owner string) *liveRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[id]; run != nil && run.owner == owner {
		return run
	}
	return nil
}

// Stop cancels the running turn of a conversation. Tools already executed are
// not rolled back.
func (m *Module) Stop(id, owner string) bool {
	run := m.live(id, owner)
	if run != nil {
		run.cancel()
	}
	return run != nil
}

// Wait blocks until the running turn of a conversation ends (tests and
// shutdown use it).
func (m *Module) Wait(ctx context.Context, id string) error {
	m.mu.Lock()
	run := m.runs[id]
	m.mu.Unlock()
	if run == nil {
		return nil
	}
	for {
		_, done, changed := run.next(0)
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
