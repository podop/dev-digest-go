package domain

import "encoding/json"

// ClassificationSchemaJSON is the JSON Schema of the classification call (spec 7.12, schema
// IntentClassification). It equals, byte for byte, the output of Node's toJsonSchema of the Zod
// IntentClassification (zodResponseFormat), property order included: every property is required,
// so llm.StrictSchema leaves it unchanged (testdata/intent_schema.node.json). The change_type
// enum is the shared IntentChangeType contract.
var ClassificationSchemaJSON = json.RawMessage(`{"type":"object","properties":{` +
	`"intent":{"type":"string","description":"One or two plain English sentences: what this PR is trying to accomplish and why."},` +
	`"in_scope":{"type":"array","items":{"type":"string"},"description":"Short bullets of what this PR intentionally changes."},` +
	`"out_of_scope":{"type":"array","items":{"type":"string"},"description":"Short bullets of what this PR explicitly does NOT intend to change (may be empty)."},` +
	`"change_type":{"type":"string","enum":["feature","bugfix","refactor","docs","test","chore","security","perf","mixed"],` +
	`"description":"The single dominant kind of change in this PR."}},` +
	`"required":["intent","in_scope","out_of_scope","change_type"],"additionalProperties":false,` +
	`"$schema":"http://json-schema.org/draft-07/schema#"}`)
