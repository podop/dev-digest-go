package domain

// CompareSide is one finished run with the results compare reads from it; the run carries the
// frozen configuration.
type CompareSide struct {
	Run     Run
	Results []CaseResult
}

// notComparable is the 422 runs_not_comparable failure with the message of the broken rule.
func notComparable(message string) error {
	return unprocessable(CodeNotComparable, message, nil)
}

// AssertComparable checks that two runs belong to one owner (one agent or one skill) and both
// finished, in this order: same owner kind, same agent (agent runs) or skill (skill runs), neither
// still running. The error is a *RuleError (422 runs_not_comparable).
func AssertComparable(a, b Run) error {
	if a.OwnerKind != b.OwnerKind {
		return notComparable("Runs of different owner kinds cannot be compared")
	}
	if a.OwnerKind == OwnerSkill {
		if !equalPtr(a.SkillID, b.SkillID) {
			return notComparable("Runs of different skills cannot be compared")
		}
	} else if a.AgentID != b.AgentID {
		return notComparable("Runs of different agents cannot be compared")
	}
	if a.Status == RunRunning || b.Status == RunRunning {
		return notComparable("A run that has not finished cannot be compared")
	}
	return nil
}

// modelChange is the model label pair: nil when provider and model are equal, the model ids when
// only the model changed, "provider/model" when the provider changed.
func modelChange(a, b Config) *ValueChange {
	if a.Provider == b.Provider {
		if a.Model == b.Model {
			return nil
		}
		return &ValueChange{A: a.Model, B: b.Model}
	}
	return &ValueChange{A: a.Provider + "/" + a.Model, B: b.Provider + "/" + b.Model}
}

func skillNames(c Config) []string {
	names := make([]string, 0, len(c.Skills))
	for _, s := range c.Skills {
		names = append(names, s.Name)
	}
	return names
}

// namesNotIn returns the names that are not in the other list, in order, duplicates kept; never nil.
func namesNotIn(names, other []string) []string {
	set := make(map[string]struct{}, len(other))
	for _, n := range other {
		set[n] = struct{}{}
	}
	out := []string{}
	for _, n := range names {
		if _, ok := set[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// CompareRuns compares run A (older) with run B (newer), spec 7.5.9: the metric deltas (B - A),
// the per-case statuses and the configuration diff. A case present in one run only has status
// CaseAbsent on the other side and counts as flipped. Cases are A's results in order followed by
// the cases only B has.
func CompareRuns(a, b CompareSide) (Compare, error) {
	if err := AssertComparable(a.Run, b.Run); err != nil {
		return Compare{}, err
	}
	cases := make([]CompareCase, 0, len(a.Results)+len(b.Results))
	index := make(map[string]int, len(a.Results)+len(b.Results))
	for _, r := range a.Results {
		if i, ok := index[r.CaseID]; ok { // a repeated case id overwrites, as Map.set does
			cases[i].CaseName, cases[i].A = r.CaseName, r.Status
			continue
		}
		index[r.CaseID] = len(cases)
		cases = append(cases, CompareCase{CaseID: r.CaseID, CaseName: r.CaseName, A: r.Status, B: CaseAbsent})
	}
	for _, r := range b.Results {
		if i, ok := index[r.CaseID]; ok {
			cases[i].B = r.Status
			continue
		}
		index[r.CaseID] = len(cases)
		cases = append(cases, CompareCase{CaseID: r.CaseID, CaseName: r.CaseName, A: CaseAbsent, B: r.Status})
	}
	for i := range cases {
		cases[i].Flipped = cases[i].A != cases[i].B
	}

	ca, cb := a.Run.Config, b.Run.Config
	namesA, namesB := skillNames(ca), skillNames(cb)
	diff := ConfigDiff{
		SystemPrompt:  LineDiff(ca.SystemPrompt, cb.SystemPrompt),
		Model:         modelChange(ca, cb),
		SkillsAdded:   namesNotIn(namesB, namesA),
		SkillsRemoved: namesNotIn(namesA, namesB),
	}
	if ca.Strategy != cb.Strategy {
		diff.Strategy = &ValueChange{A: ca.Strategy, B: cb.Strategy}
	}
	ma, mb := a.Run.Metrics, b.Run.Metrics
	return Compare{
		A: a.Run,
		B: b.Run,
		Delta: Delta{
			Recall:           MetricChange(ma.Recall, mb.Recall, MetricDecimals),
			Precision:        MetricChange(ma.Precision, mb.Precision, MetricDecimals),
			CitationAccuracy: MetricChange(ma.CitationAccuracy, mb.CitationAccuracy, MetricDecimals),
			CostUSD:          MetricChange(ma.CostUSD, mb.CostUSD, CostDecimals),
		},
		Cases:      cases,
		ConfigDiff: diff,
	}, nil
}
