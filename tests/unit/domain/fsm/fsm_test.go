package fsm_test

import (
	"testing"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
	"github.com/mathmach/gpu-grid-worker/pkg/domain/fsm"
)

type sampleState string

const (
	statePending    sampleState = "PENDING"
	stateProcessing sampleState = "PROCESSING"
	stateCompleted  sampleState = "COMPLETED"
	stateFailed     sampleState = "FAILED"
	stateUnknown    sampleState = "UNKNOWN"
)

func TestTableIdempotentTransitionsAndTerminalStateEnforcement(t *testing.T) {
	table := fsm.NewTable("sample_job", true, map[sampleState][]sampleState{
		statePending:    {stateProcessing, stateFailed},
		stateProcessing: {stateCompleted, stateFailed},
		stateCompleted:  nil,
		stateFailed:     {},
	})

	next, err := table.Transition(statePending, stateProcessing)
	if err != nil || next != stateProcessing {
		t.Fatalf("expected PENDING -> PROCESSING to succeed, got next=%s err=%v", next, err)
	}
	same, err := table.Transition(stateProcessing, stateProcessing)
	if err != nil || same != stateProcessing {
		t.Fatalf("expected idempotent PROCESSING -> PROCESSING to succeed, got next=%s err=%v", same, err)
	}
	done, err := table.Transition(stateProcessing, stateCompleted)
	if err != nil || done != stateCompleted {
		t.Fatalf("expected PROCESSING -> COMPLETED to succeed, got next=%s err=%v", done, err)
	}
	if !table.CanTransition(stateCompleted, stateCompleted) || !table.CanTransition(stateFailed, stateFailed) {
		t.Fatal("expected known terminal states to permit idempotent self-transitions when allowIdemp=true")
	}

	if table.CanTransition(stateCompleted, statePending) || table.CanTransition(stateCompleted, stateFailed) {
		t.Fatal("expected terminal COMPLETED state to reject outbound transitions")
	}
	stay, termErr := table.Transition(stateCompleted, stateProcessing)
	if termErr == nil || stay != stateCompleted {
		t.Fatalf("expected illegal transition from terminal state to preserve original state, got stay=%s err=%v", stay, termErr)
	}
	appErr, ok := derr.AsAppError(termErr)
	if !ok || appErr.Code != derr.CodeIllegalState || appErr.HTTPStatus != 409 {
		t.Fatalf("expected 409 CodeIllegalState AppError, got ok=%v err=%+v", ok, appErr)
	}
	if appErr.Params["entity"] != "sample_job" || appErr.Params["from"] != "COMPLETED" || appErr.Params["to"] != "PROCESSING" {
		t.Fatalf("unexpected transition error params: %+v", appErr.Params)
	}

	if table.CanTransition(stateUnknown, stateUnknown) {
		t.Fatal("expected unknown state outside transitions map to reject idempotent self-transition")
	}
	if table.CanTransition("", "") {
		t.Fatal("expected empty state to reject idempotent self-transition")
	}
}

func TestTableStrictNonIdempotentModeRejectsSelfTransition(t *testing.T) {
	strict := fsm.NewTable("worker_session", false, map[sampleState][]sampleState{
		statePending:   {stateCompleted},
		stateCompleted: nil,
	})

	if strict.CanTransition(statePending, statePending) || strict.CanTransition(stateCompleted, stateCompleted) {
		t.Fatal("expected allowIdemp=false table to reject implicit self-transitions")
	}
	orig, err := strict.Transition(statePending, statePending)
	if err == nil || orig != statePending || derr.StatusOf(err) != 409 {
		t.Fatalf("expected 409 on self-transition when allowIdemp=false, got orig=%s err=%v", orig, err)
	}
	done, err := strict.Transition(statePending, stateCompleted)
	if err != nil || done != stateCompleted {
		t.Fatalf("expected PENDING -> COMPLETED to succeed, got done=%s err=%v", done, err)
	}
}
