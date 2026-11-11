package fsm

import "github.com/mathmach/gpu-grid-worker/pkg/domain/derr"

type Table[S ~string] struct {
	entity      string
	allowIdemp  bool
	transitions map[S][]S
}

func NewTable[S ~string](entity string, allowIdemp bool, transitions map[S][]S) Table[S] {
	return Table[S]{
		entity:      entity,
		allowIdemp:  allowIdemp,
		transitions: transitions,
	}
}

func (t Table[S]) CanTransition(from, to S) bool {
	if t.allowIdemp && from == to && to != "" {
		_, known := t.transitions[from]
		return known
	}
	for _, allowed := range t.transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

func (t Table[S]) Transition(from, to S) (S, error) {
	if !t.CanTransition(from, to) {
		return from, derr.New(derr.CodeIllegalState, 409, t.entity+" cannot transition from "+string(from)+" to "+string(to), map[string]any{
			"entity": t.entity,
			"from":   string(from),
			"to":     string(to),
		})
	}
	return to, nil
}
