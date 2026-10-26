//go:build !race

package domain_test

// raceEnabled is false without the race detector; see race_on_test.go.
const raceEnabled = false
