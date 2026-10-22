package domain

// Staleness of a stored tour (spec 7.10.6, Node's staleness()): a different indexed commit wins
// over a changed prompt version.

// Staleness returns why a tour stored for storedSHA with storedPromptVersion is out of date, or
// StaleNone. currentSHA is the index state's last indexed SHA; "" is "no commit known" and
// differs from every stored SHA, as Node's null does (so a repository whose index was reset
// reads as index_changed). The current prompt version is PromptVersion.
func Staleness(storedSHA string, storedPromptVersion int, currentSHA string) StaleReason {
	if currentSHA == "" || currentSHA != storedSHA {
		return StaleIndexChanged
	}
	if storedPromptVersion != PromptVersion {
		return StalePromptChanged
	}
	return StaleNone
}

// StateOf is the read answer for a stored tour: ready, stale or not, against the current SHA.
func StateOf(t Tour, currentSHA string) State {
	reason := Staleness(t.IndexedSHA, t.PromptVersion, currentSHA)
	return State{Tour: &t, Stale: reason != StaleNone, StaleReason: reason}
}
