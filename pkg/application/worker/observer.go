package worker

import "sync"

func (e *Engine) Subscribe() (<-chan Snapshot, func()) {
	ch := make(chan Snapshot, 1)

	e.obsMu.Lock()
	e.nextSubID++
	id := e.nextSubID
	if e.subscribers == nil {
		e.subscribers = make(map[uint64]chan Snapshot)
	}
	e.subscribers[id] = ch
	e.obsMu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			e.obsMu.Lock()
			if existing, ok := e.subscribers[id]; ok {
				delete(e.subscribers, id)
				close(existing)
			}
			e.obsMu.Unlock()
		})
	}
	return ch, unsubscribe
}

func (e *Engine) notifyObservers() {
	e.obsMu.Lock()
	hasSubs := len(e.subscribers) > 0
	e.obsMu.Unlock()
	if !hasSubs {
		return
	}

	snap := e.Snapshot()

	e.obsMu.Lock()
	for _, ch := range e.subscribers {
		pushLatestSnapshot(ch, snap)
	}
	e.obsMu.Unlock()
}

func pushLatestSnapshot(ch chan Snapshot, snap Snapshot) {
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- snap:
	default:
	}
}
