package main

import (
	"context"
	"fmt"
	"math/big"

	spenddomain "dev-digest-go/backend/internal/spend/domain"
)

// spendCapKey is the Settings key that holds a workspace's monthly spend cap in US dollars (a JSON
// number with at most 2 decimals, or null for no cap).
const spendCapKey = "monthly_spend_cap_usd"

// microsPerUSD mirrors spenddomain.Micros: one dollar is a million of them.
const microsPerUSD = 1_000_000

// settingsCaps adapts the settings use case to the spend context's Caps port, so settings never
// imports spend and spend never imports settings: only this composition root knows both.
type settingsCaps struct{ settings settingsBag }

// Cap returns the workspace's monthly cap. A missing or null key is nil (no cap). The number is
// converted exactly, from its JSON text, never through float64; a value that is not a number, is
// negative, or does not fit whole Micros is an error, so the spend check fails closed (D16).
func (c settingsCaps) Cap(ctx context.Context, workspaceID string) (*spenddomain.Micros, error) {
	bag, err := c.settings.Get(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	raw, ok := bag[spendCapKey]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	m, err := parseCapMicros(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spendCapKey, err)
	}
	return &m, nil
}

// parseCapMicros converts the JSON text of a dollar amount to Micros without rounding.
func parseCapMicros(lit string) (spenddomain.Micros, error) {
	usd, ok := new(big.Rat).SetString(lit)
	if !ok {
		return 0, fmt.Errorf("not a number: %q", lit)
	}
	if usd.Sign() < 0 {
		return 0, fmt.Errorf("negative amount: %q", lit)
	}
	micros := new(big.Rat).Mul(usd, big.NewRat(microsPerUSD, 1))
	if !micros.IsInt() || !micros.Num().IsInt64() {
		return 0, fmt.Errorf("amount %q does not fit whole millionths of a dollar", lit)
	}
	return spenddomain.Micros(micros.Num().Int64()), nil
}
