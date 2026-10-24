package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	settingsapp "dev-digest-go/backend/internal/settings/app"
	spenddomain "dev-digest-go/backend/internal/spend/domain"
)

func TestParseCapMicros(t *testing.T) {
	tests := []struct {
		name    string
		lit     string
		want    spenddomain.Micros
		wantErr bool
	}{
		{name: "whole dollars", lit: "50", want: 50_000_000},
		{name: "two decimals", lit: "12.34", want: 12_340_000},
		{name: "drifting float", lit: "0.07", want: 70_000},
		{name: "one cent", lit: "0.01", want: 10_000},
		{name: "zero", lit: "0", want: 0},
		{name: "max", lit: "1000000", want: 1_000_000_000_000},
		{name: "exponent form", lit: "1.5e1", want: 15_000_000},
		{name: "six decimals fit", lit: "0.000001", want: 1},
		{name: "seven decimals do not", lit: "0.0000001", wantErr: true},
		{name: "negative", lit: "-1", wantErr: true},
		{name: "not a number", lit: `"ten"`, wantErr: true},
		{name: "overflow", lit: "1e30", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCapMicros(tc.lit)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseCapMicros(%s) error = %v, want error %t", tc.lit, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseCapMicros(%s) = %d, want %d", tc.lit, got, tc.want)
			}
		})
	}
}

func TestSettingsCaps_Cap(t *testing.T) {
	micros := func(m spenddomain.Micros) *spenddomain.Micros { return &m }
	tests := []struct {
		name    string
		src     fakeBag
		want    *spenddomain.Micros
		wantErr bool
	}{
		{name: "key missing is no cap", src: fakeBag{bag: settingsapp.Bag{}}},
		{name: "null is no cap", src: fakeBag{bag: settingsapp.Bag{spendCapKey: json.RawMessage("null")}}},
		{name: "number", src: fakeBag{bag: settingsapp.Bag{spendCapKey: json.RawMessage("25.5")}}, want: micros(25_500_000)},
		{name: "zero is a cap of zero", src: fakeBag{bag: settingsapp.Bag{spendCapKey: json.RawMessage("0")}}, want: micros(0)},
		{name: "string is an error", src: fakeBag{bag: settingsapp.Bag{spendCapKey: json.RawMessage(`"5"`)}}, wantErr: true},
		{name: "read error fails closed", src: fakeBag{err: errors.New("db down")}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := settingsCaps{settings: tc.src}.Cap(t.Context(), "ws")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Cap() error = %v, want error %t", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Cap() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOrDev(t *testing.T) {
	if got := orDev(""); got != "dev" {
		t.Errorf("orDev(\"\") = %q, want dev", got)
	}
	if got := orDev("1.2.3"); got != "1.2.3" {
		t.Errorf("orDev(1.2.3) = %q, want 1.2.3", got)
	}
}
