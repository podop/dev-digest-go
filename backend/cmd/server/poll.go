package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pullsdomain "dev-digest-go/backend/internal/pulls/domain"
	settingsapp "dev-digest-go/backend/internal/settings/app"
)

// pollingIntervalKey is the Settings key that holds a workspace's background-poll interval, in
// minutes (a JSON number, default row 5).
const pollingIntervalKey = "polling_interval_min"

// settingsBag reads every setting of a workspace (*settingsapp.Service).
type settingsBag interface {
	Get(ctx context.Context, workspaceID string) (settingsapp.Bag, error)
}

// settingsIntervals adapts the settings use case to the pulls context's IntervalSource port, so
// settings never imports pulls and pulls never imports settings: only this composition root knows both.
type settingsIntervals struct{ settings settingsBag }

// Interval returns the workspace's polling_interval_min as a duration. A missing or null key
// is 0, which the poller reads as its default; a value that is not a number is an error (the
// poller logs it and uses the default). Values above the poller's ceiling are cut to it, so the
// conversion never overflows.
func (s settingsIntervals) Interval(ctx context.Context, workspaceID string) (time.Duration, error) {
	bag, err := s.settings.Get(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("read settings: %w", err)
	}
	raw, ok := bag[pollingIntervalKey]
	if !ok || string(raw) == "null" {
		return 0, nil
	}
	var minutes float64
	if err := json.Unmarshal(raw, &minutes); err != nil {
		return 0, fmt.Errorf("%s: not a number: %w", pollingIntervalKey, err)
	}
	if minutes <= 0 {
		return 0, nil
	}
	return time.Duration(min(minutes, pullsdomain.MaxPollInterval.Minutes()) * float64(time.Minute)), nil
}
