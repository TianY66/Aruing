package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/Aruing/Aruing/internal/core"
)

// NamespaceClarificationSeed carries an all-namespace observation into the
// formal run so it can be resumed without repeating the lookup.
type NamespaceClarificationSeed struct {
	Tasks    []core.Task
	Evidence []core.Evidence
}

// NamespaceAmbiguitySuspender lets an executor create a persisted resolve-stage
// suspension from an ambiguity detected by the chat tool loop.
type NamespaceAmbiguitySuspender interface {
	SuspendForNamespaceAmbiguity(
		ctx context.Context,
		run core.Run,
		seed NamespaceClarificationSeed,
	) (core.Outcome, error)
}

// ClarifyNamespaceAmbiguity asks the executor to suspend only when the seeded
// all-namespace result contains a matching ambiguous target. If the query does
// not refer to those duplicate names, the chat loop can continue normally.
func ClarifyNamespaceAmbiguity(
	ctx context.Context,
	factory *core.Factory,
	executor RunExecutor,
	ledger RunLedger,
	sessionID, question string,
	seed NamespaceClarificationSeed,
) (RespondOutput, bool, error) {
	if err := ctx.Err(); err != nil {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: %w", err)
	}
	if factory == nil {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: factory is nil")
	}
	if executor == nil {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: executor is nil")
	}
	if ledger == nil {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: ledger is nil")
	}
	if strings.TrimSpace(sessionID) == "" {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: session id is required")
	}
	if strings.TrimSpace(question) == "" {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: question is required")
	}
	suspender, ok := executor.(NamespaceAmbiguitySuspender)
	if !ok {
		return RespondOutput{}, false, fmt.Errorf("namespace clarification: executor %T cannot suspend on namespace ambiguity", executor)
	}
	runID, err := factory.NewID("run")
	if err != nil {
		return RespondOutput{}, false, fmt.Errorf("new run id: %w", err)
	}
	now := factory.Now()
	run := core.Run{
		ID:        runID,
		SessionID: sessionID,
		Question:  question,
		Status:    core.RunStatusCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}
	outcome, err := suspender.SuspendForNamespaceAmbiguity(ctx, run, seed)
	if err != nil {
		return RespondOutput{}, false, fmt.Errorf("suspend namespace ambiguity: %w", err)
	}
	if outcome.Suspension == nil {
		return RespondOutput{}, false, nil
	}
	respond, err := outcomeToRespond(ctx, ledger, sessionID, question, run.ID, outcome)
	if err != nil {
		return RespondOutput{}, false, err
	}
	return respond, true, nil
}
