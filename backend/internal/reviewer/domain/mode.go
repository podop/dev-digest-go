package domain

// SelectMode picks how a review runs from the agent's strategy (spec FR4023): single-pass is one
// call; map-reduce is one call per file when the diff has more than one file; auto goes
// map-reduce only when the diff is both large (more than threshold added plus deleted lines) and
// multi-file. Any other strategy value behaves like auto, as it does in the Node engine.
func SelectMode(strategy Strategy, d Diff, threshold int) Mode {
	switch strategy {
	case StrategySinglePass:
		return ModeSinglePass
	case StrategyMapReduce:
		if len(d.Files) > 1 {
			return ModeMapReduce
		}
		return ModeSinglePass
	}
	if d.ChangedLines() > threshold && len(d.Files) > 1 {
		return ModeMapReduce
	}
	return ModeSinglePass
}

// EffectiveMode is the mode a review reports once its chunks are known: a review that ends up with
// more than one chunk (a split oversize pass) is reduced, and reported, as map-reduce.
func EffectiveMode(selected Mode, chunks int) Mode {
	if chunks > 1 {
		return ModeMapReduce
	}
	return selected
}

// ChunksFor makes the chunks a mode starts from, before the size cap: one per file for
// map-reduce, the whole diff for a single pass.
func ChunksFor(mode Mode, d Diff) []Chunk {
	if mode == ModeMapReduce {
		return FileChunks(d)
	}
	return []Chunk{WholeDiffChunk(d)}
}
