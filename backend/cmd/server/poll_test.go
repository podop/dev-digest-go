package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	settingsapp "dev-digest-go/backend/internal/settings/app"
)

type fakeBag struct {
	bag settingsapp.Bag
	err error
}

func (f fakeBag) Get(context.Context, string) (settingsapp.Bag, error) { return f.bag, f.err }

func TestSettingsIntervals_Interval(t *testing.T) {
	tests := []struct {
		name    string
		bag     settingsapp.Bag
		want    time.Duration
		wantErr bool
	}{
		{name: "missing key", bag: settingsapp.Bag{}, want: 0},
		{name: "null", bag: settingsapp.Bag{pollingIntervalKey: json.RawMessage("null")}, want: 0},
		{name: "five minutes", bag: settingsapp.Bag{pollingIntervalKey: json.RawMessage("5")}, want: 5 * time.Minute},
		{name: "one minute", bag: settingsapp.Bag{pollingIntervalKey: json.RawMessage("1")}, want: time.Minute},
		{name: "non-positive", bag: settingsapp.Bag{pollingIntervalKey: json.RawMessage("0")}, want: 0},
		{name: "above the ceiling", bag: settingsapp.Bag{pollingIntervalKey: json.RawMessage("1e30")}, want: 24 * time.Hour},
		{name: "not a number", bag: settingsapp.Bag{pollingIntervalKey: json.RawMessage(`"soon"`)}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := settingsIntervals{fakeBag{bag: tc.bag}}.Interval(t.Context(), "ws")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Interval(%v) error = %v, wantErr %t", tc.bag, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Interval(%v) = %v, want %v", tc.bag, got, tc.want)
			}
		})
	}
}

func TestSettingsIntervals_StoreError(t *testing.T) {
	boom := errors.New("boom")
	if _, err := (settingsIntervals{fakeBag{err: boom}}).Interval(t.Context(), "ws"); !errors.Is(err, boom) {
		t.Errorf("Interval() error = %v, want it to wrap %v", err, boom)
	}
}
