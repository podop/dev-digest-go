package domain

// The derivation cache key (spec 7.4.1; Node intent/domain/intent.ts buildCacheKeyInput and
// canonicalJson, intent-service.ts sha256Hex). The hash is stored as pr_intent.input_hash and is
// compared byte for byte with rows the Node server wrote, so the canonical JSON follows
// JSON.stringify exactly.

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// CanonicalJSON is the key input as JavaScript's JSON.stringify(sortKeysDeep(input)): keys sorted,
// no whitespace, no HTML escaping, U+2028 and U+2029 written as they are. Ticket refs are put in
// ascending order and doc paths in UTF-16 code unit order here, on copies, so the result does not
// depend on the order the caller used. A ticket number that is not finite (Number("9" * 400) is
// Infinity) is written as null, as JSON.stringify does.
func (k KeyInput) CanonicalJSON() (string, error) {
	refs := append([]float64{}, k.TicketRefs...)
	sort.Float64s(refs)
	ticketRefs := make([]any, len(refs))
	for i, r := range refs {
		if math.IsInf(r, 0) || math.IsNaN(r) {
			continue // nil: JSON null
		}
		ticketRefs[i] = r
	}
	paths := append([]string{}, k.DocPaths...)
	sort.Slice(paths, func(i, j int) bool { return reviewer.LessUnits(paths[i], paths[j]) })

	return reviewer.Canonical(map[string]any{
		"prompt_version": k.PromptVersion,
		"provider":       k.Provider,
		"model":          k.Model,
		"title":          k.Title,
		"body":           k.Body,
		"branch":         k.Branch,
		"head_sha":       k.HeadSHA,
		"ticket_refs":    ticketRefs,
		"doc_paths":      paths,
	})
}

// Hash is the lowercase hex SHA-256 of the canonical JSON's UTF-8 bytes: pr_intent.input_hash.
func (k KeyInput) Hash() (string, error) {
	canon, err := k.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:]), nil
}

// NewKeyInput builds the key material of a derivation from the resolved model, the pull request
// and the extracted references (the PromptVersion constant is set here).
func NewKeyInput(provider, model string, pull Pull, tickets []TicketRef, docs []DocRef) KeyInput {
	k := KeyInput{
		Body: pull.Body, Branch: pull.Branch, HeadSHA: pull.HeadSHA, Model: model,
		PromptVersion: PromptVersion, Provider: provider, Title: pull.Title,
		DocPaths: make([]string, len(docs)), TicketRefs: make([]float64, len(tickets)),
	}
	for i, d := range docs {
		k.DocPaths[i] = d.Path
	}
	for i, t := range tickets {
		k.TicketRefs[i] = t.Number
	}
	return k
}

// correlationHashChars is the length of the hash prefix in the prompt log's correlation id.
const correlationHashChars = 12

// CorrelationID is the correlation id of the derivation's prompt log line:
// intent:<pr id>:<first 12 hex characters of the hash>.
func CorrelationID(prID, hash string) string {
	if len(hash) > correlationHashChars {
		hash = hash[:correlationHashChars]
	}
	return "intent:" + prID + ":" + hash
}
