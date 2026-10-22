package domain

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ReviewSchemaName is the structured-output name of the review call (also the mock provider's
// fixture key).
const ReviewSchemaName = "Review"

// reviewRef is the definition name zod-to-json-schema gives the trifecta component enum that the
// evidence items reuse by reference.
const reviewRef = "Review_properties_findings_items_properties_trifecta_components_anyOf_0_items"

// The three descriptions the Zod contract (frontend contracts findings.ts) attaches to the Review
// schema; models see them in the provider schema.
const (
	scoreDescription = "Overall PR quality from 0 to 100, where HIGHER is better. 90–100 = no or only trivial issues (approve); " +
		"60–89 = minor suggestions; 30–59 = warnings worth addressing; 0–29 = critical problems. " +
		"Must be consistent with `findings`: if there are no findings, the score is 90 or above."
	skillDescription = "If this finding enforces a rule from a skill listed under \"## Skills / rules\", the exact name " +
		"from that skill's ### heading; otherwise null."
	outOfScopeDescription = "true when this finding is outside the \"## PR intent\" section's stated in-scope changes (only " +
		"meaningful when that section is present in this prompt — otherwise leave unset). Flagging a " +
		"finding this way NEVER means omitting it, downgrading its severity, or softening its rationale: " +
		"always report every real defect at its true severity regardless of stated scope."
)

// ReviewSchemaJSON is the JSON Schema of a Review in the form the Zod contract produces for
// validation: optional fields (suggestion, kind, trifecta_components, evidence, skill,
// out_of_scope) are not in "required" and accept null. llm.StrictSchema turns it into exactly
// ReviewProviderSchemaJSON, the document the Node server sends (D4010). Null is written as
// "nullable": true for strings and booleans and as anyOf for the rest, as the Node converter
// does, so the validator must honour "nullable"; ReviewValidationSchemaJSON is the same schema
// with anyOf alone for a validator that does not.
var ReviewSchemaJSON = json.RawMessage(buildReviewSchema(false, true))

// ReviewProviderSchemaJSON is the strict schema providers receive: every property of every object
// required, additionalProperties false. It equals, as JSON and byte for byte, the output of Node's
// toJsonSchema(Review, 'Review') (OpenAI's Zod converter), property order included.
var ReviewProviderSchemaJSON = json.RawMessage(buildReviewSchema(true, true))

// ReviewValidationSchemaJSON is ReviewSchemaJSON with every nullable written as anyOf with a null
// alternative, which a validator without "nullable" support understands. It is not a provider schema.
var ReviewValidationSchemaJSON = json.RawMessage(buildReviewSchema(false, false))

// buildReviewSchema writes the schema. strict lists every finding property as required;
// nullableKeyword writes null-accepting scalars as "nullable": true rather than anyOf.
func buildReviewSchema(strict, nullableKeyword bool) string {
	str := func(v any) string { // JSON text of a scalar, without HTML escaping
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return strings.TrimSuffix(b.String(), "\n")
	}
	// nullable scalar with an optional description
	nullable := func(typ, description string) string {
		desc := ""
		if description != "" {
			desc = `,"description":` + str(description)
		}
		if nullableKeyword {
			return `{"type":"` + typ + `","nullable":true` + desc + `}`
		}
		return `{"anyOf":[{"type":"` + typ + `"},{"type":"null"}]` + desc + `}`
	}
	anyOfNull := func(schema string) string { return `{"anyOf":[` + schema + `,{"type":"null"}]}` }

	trifecta := `{"type":"string","enum":["private_data_access","untrusted_input","exfil_path"]}`
	required := `"id","severity","category","title","file","start_line","end_line","rationale","confidence"`
	if strict {
		required = `"id","severity","category","title","file","start_line","end_line","rationale","suggestion",` +
			`"confidence","kind","trifecta_components","evidence","skill","out_of_scope"`
	}
	finding := `{"type":"object","properties":{` +
		`"id":{"type":"string"},` +
		`"severity":{"type":"string","enum":["CRITICAL","WARNING","SUGGESTION"]},` +
		`"category":{"type":"string","enum":["bug","security","perf","style","test"]},` +
		`"title":{"type":"string"},` +
		`"file":{"type":"string"},` +
		`"start_line":{"type":"integer"},` +
		`"end_line":{"type":"integer"},` +
		`"rationale":{"type":"string"},` +
		`"suggestion":` + nullable("string", "") + `,` +
		`"confidence":{"type":"number","minimum":0,"maximum":1},` +
		`"kind":` + anyOfNull(`{"type":"string","enum":["finding","secret_leak","lethal_trifecta","phantom","hook"]}`) + `,` +
		`"trifecta_components":` + anyOfNull(`{"type":"array","items":`+trifecta+`}`) + `,` +
		`"evidence":` + anyOfNull(`{"type":"array","items":{"type":"object","properties":{`+
		`"component":{"$ref":"#/definitions/`+reviewRef+`"},"file":{"type":"string"},"line":{"type":"integer"}},`+
		`"required":["component","file","line"],"additionalProperties":false}}`) + `,` +
		`"skill":` + nullable("string", skillDescription) + `,` +
		`"out_of_scope":` + nullable("boolean", outOfScopeDescription) +
		`},"required":[` + required + `],"additionalProperties":false}`

	body := `"type":"object","properties":{` +
		`"verdict":{"type":"string","enum":["request_changes","approve","comment"]},` +
		`"summary":{"type":"string"},` +
		`"score":{"type":"integer","minimum":0,"maximum":100,"description":` + str(scoreDescription) + `},` +
		`"findings":{"type":"array","items":` + finding + `}},` +
		`"required":["verdict","summary","score","findings"],"additionalProperties":false`

	return `{` + body + `,"definitions":{"` + reviewRef + `":` + trifecta + `,"Review":{` + body + `}},` +
		`"$schema":"http://json-schema.org/draft-07/schema#"}`
}
