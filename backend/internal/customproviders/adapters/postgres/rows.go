package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm/custom"
)

const (
	// uniqueViolation is the SQLState of a duplicate key.
	uniqueViolation = "23505"
)

// providerColumns lists the columns of custom_providers as providerRow reads them. The header
// names come back as JSON text, which keeps the driver's array types out of this package.
const providerColumns = `workspace_id::text AS workspace_id, id, name, protocol, base_url, auth_mode, auth_header,
	to_json(extra_header_names)::text AS extra_header_names, structured_output, created_at, updated_at`

// providerRow is a row of custom_providers. id is the bare slug.
type providerRow struct {
	WorkspaceID      string    `gorm:"column:workspace_id"`
	ID               string    `gorm:"column:id"`
	Name             string    `gorm:"column:name"`
	Protocol         string    `gorm:"column:protocol"`
	BaseURL          string    `gorm:"column:base_url"`
	AuthMode         string    `gorm:"column:auth_mode"`
	AuthHeader       *string   `gorm:"column:auth_header"`
	ExtraHeaderNames string    `gorm:"column:extra_header_names"`
	StructuredOutput string    `gorm:"column:structured_output"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

// modelColumns lists the columns of custom_provider_models as modelRow reads them. Prices are read
// as text so a numeric never passes through a lossy driver type.
const modelColumns = `provider_id, model_id, label, prompt_per_m::text AS prompt_per_m,
	completion_per_m::text AS completion_per_m`

// modelRow is a row of custom_provider_models.
type modelRow struct {
	ProviderID     string  `gorm:"column:provider_id"`
	ModelID        string  `gorm:"column:model_id"`
	Label          *string `gorm:"column:label"`
	PromptPerM     *string `gorm:"column:prompt_per_m"`
	CompletionPerM *string `gorm:"column:completion_per_m"`
}

// toDomain maps a provider row and its models (already in order) to the domain type.
func (r providerRow) toDomain(models []modelRow) (domain.Provider, error) {
	names := []string{}
	if err := json.Unmarshal([]byte(r.ExtraHeaderNames), &names); err != nil {
		return domain.Provider{}, fmt.Errorf("decode extra header names of %s: %w", r.ID, err)
	}
	p := domain.Provider{
		ID:               custom.ID(r.ID),
		Name:             r.Name,
		Protocol:         custom.Protocol(r.Protocol),
		BaseURL:          r.BaseURL,
		AuthMode:         custom.AuthMode(r.AuthMode),
		StructuredOutput: custom.Mode(r.StructuredOutput),
		ExtraHeaderNames: names,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
	if r.AuthHeader != nil {
		p.AuthHeader = *r.AuthHeader
	}
	for _, m := range models {
		dm, err := m.toDomain()
		if err != nil {
			return domain.Provider{}, err
		}
		p.Models = append(p.Models, dm)
	}
	return p, nil
}

func (m modelRow) toDomain() (domain.Model, error) {
	dm := domain.Model{ID: m.ModelID, Label: m.Label}
	if m.PromptPerM == nil || m.CompletionPerM == nil {
		return dm, nil
	}
	prompt, err := strconv.ParseFloat(*m.PromptPerM, 64)
	if err != nil {
		return domain.Model{}, fmt.Errorf("decode prompt price of model %s: %w", m.ModelID, err)
	}
	completion, err := strconv.ParseFloat(*m.CompletionPerM, 64)
	if err != nil {
		return domain.Model{}, fmt.Errorf("decode completion price of model %s: %w", m.ModelID, err)
	}
	dm.Pricing = &domain.Pricing{PromptPerM: prompt, CompletionPerM: completion}
	return dm, nil
}

// priceText writes a price with exactly the six decimals the column allows. A price the domain
// accepted (at most six decimals) round-trips exactly: ParseFloat of the text gives the same float64.
func priceText(v float64) string { return strconv.FormatFloat(v, 'f', priceScale, 64) }

// priceScale is the number of decimals of a stored price (the column's CHECK allows six).
const priceScale = 6

// textArray renders a Postgres text[] literal ({"a","b"}), quoting every element.
func textArray(items []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// isUniqueViolation reports whether err is a duplicate-key error (SQLState 23505); the driver's
// error type exposes it as a method.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}

// nullIfEmpty maps "" to SQL NULL.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullTime maps the zero time to SQL NULL, which the statements turn into now().
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
