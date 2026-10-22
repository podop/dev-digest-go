package domain

import (
	"errors"
	"fmt"
)

// TargetMode says how a review request names its agents.
type TargetMode int

// The target modes. A request that names nothing never gets a plan (see PlanTargets).
const (
	// TargetAgent runs the one agent AgentID.
	TargetAgent TargetMode = iota + 1
	// TargetAll runs every enabled agent of the workspace.
	TargetAll
	// TargetAgentIDs runs the listed agents.
	TargetAgentIDs
)

// InvalidRunRequestError is a review request that names its targets wrongly. The use case maps it
// to 400 with the code invalid_run_request; the message is the response message.
type InvalidRunRequestError struct {
	Message string
}

// Error returns the response message.
func (e *InvalidRunRequestError) Error() string { return e.Message }

// ErrInvalidRunRequest matches every *InvalidRunRequestError with errors.Is.
var ErrInvalidRunRequest = errors.New("invalid run request")

// Is reports whether target is ErrInvalidRunRequest.
func (e *InvalidRunRequestError) Is(target error) bool { return target == ErrInvalidRunRequest }

// TargetPlan is the validated way a review request names its agents.
type TargetPlan struct {
	Mode TargetMode
	// AgentID is set for TargetAgent.
	AgentID string
	// AgentIDs is set for TargetAgentIDs: the ids deduplicated, in first-occurrence order.
	AgentIDs []string
}

// PlanTargets validates how a request names its agents (spec 7.2, Node resolveTargets). Only
// truthiness of agentID and all counts. agentIDs nil means the key is absent; a non-nil empty
// slice is present and empty.
//
// agentIDs is exclusive with agentID and all; its length (before deduplication) must be within
// 1..MaxAgentIDs. Otherwise all wins over agentID, which wins over nothing, and a request that
// names nothing (all:false alone) is rejected. The error is an *InvalidRunRequestError.
func PlanTargets(agentID string, all bool, agentIDs []string) (TargetPlan, error) {
	if agentIDs != nil {
		if agentID != "" || all {
			return TargetPlan{}, &InvalidRunRequestError{Message: "Provide exactly one of agentId, all or agentIds"}
		}
		if len(agentIDs) < 1 || len(agentIDs) > MaxAgentIDs {
			return TargetPlan{}, &InvalidRunRequestError{
				Message: fmt.Sprintf("agentIds must contain 1–%d ids", MaxAgentIDs),
			}
		}
		return TargetPlan{Mode: TargetAgentIDs, AgentIDs: dedupe(agentIDs)}, nil
	}
	switch {
	case all:
		return TargetPlan{Mode: TargetAll}, nil
	case agentID != "":
		return TargetPlan{Mode: TargetAgent, AgentID: agentID}, nil
	}
	return TargetPlan{}, &InvalidRunRequestError{Message: "Provide agentId, all:true or agentIds"}
}

// CreatesMultiAgentRun reports whether a request that resolved to targetCount agents also creates
// the parent multi-agent run: only a request that named agentIds and got 2 or more agents.
func (p TargetPlan) CreatesMultiAgentRun(targetCount int) bool {
	return p.Mode == TargetAgentIDs && targetCount >= 2
}

func dedupe(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
