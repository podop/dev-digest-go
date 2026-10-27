//go:build !race

package notebody_test

// raceEnabled is true when the test binary is built with -race, which slows the rules ~10x.
const raceEnabled = false
