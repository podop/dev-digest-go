package domain

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	// slugPattern is the shape the runner accepts for DEVDIGEST_AGENT.
	slugPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	slugOtherRunsRE = regexp.MustCompile(`[^a-z0-9]+`)
)

// ValidSlug reports whether s has the slug shape: a lowercase letter or digit, then at most 39
// lowercase letters, digits or dashes.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }

// Slugify derives a file-name-safe slug from a display name (spec 7.6.2): Unicode NFKD, combining
// marks U+0300-U+036F removed, lowercase, every run of characters outside a-z0-9 replaced by one
// dash, leading dashes stripped, cut to SlugMaxLength, trailing dashes stripped. A result that
// does not match the slug shape (empty after the cuts) gives fallback.
func Slugify(name, fallback string) string {
	s := norm.NFKD.String(name)
	s = strings.Map(func(r rune) rune {
		if r >= 0x0300 && r <= 0x036F {
			return -1
		}
		return r
	}, s)
	// After NFKD the only characters whose lowercase is ASCII (U+0130, U+212A) are already
	// decomposed, so the simple Go mapping agrees with JavaScript's toLowerCase for a-z0-9.
	s = strings.ToLower(s)
	s = slugOtherRunsRE.ReplaceAllString(s, "-")
	s = strings.TrimLeft(s, "-")
	if len(s) > SlugMaxLength { // only a-z0-9- is left, so bytes are UTF-16 units
		s = s[:SlugMaxLength]
	}
	s = strings.TrimRight(s, "-")
	if !ValidSlug(s) {
		return fallback
	}
	return s
}

// AgentSlug is the slug of an agent name.
func AgentSlug(name string) string { return Slugify(name, AgentSlugFallback) }

// SkillSlug is the file name stem of a skill name.
func SkillSlug(name string) string { return Slugify(name, SkillSlugFallback) }

// The names the studio and the workflow agree on (spec 7.6.3, 7.6.5).

// ManifestPath is the repository path of an agent manifest.
func ManifestPath(slug string) string { return AgentsDir + "/" + slug + ".yaml" }

// SkillPath is the repository path of a skill file.
func SkillPath(skillSlug string) string { return SkillsDir + "/" + skillSlug + ".md" }

// WorkflowFile is the file name of an agent's workflow.
func WorkflowFile(slug string) string { return "devdigest-review-" + slug + ".yml" }

// WorkflowPath is the repository path of an agent's workflow.
func WorkflowPath(slug string) string { return WorkflowsDir + "/" + WorkflowFile(slug) }

// CheckName is the workflow, job and status check name; the user marks it as a required check.
func CheckName(slug string) string { return "DevDigest Review · " + slug }

// ArtifactName is the name of the artifact the workflow uploads.
func ArtifactName(slug string) string { return "devdigest-result-" + slug }
