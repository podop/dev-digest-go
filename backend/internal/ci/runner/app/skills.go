package app

import (
	"errors"
	"path"
	"strconv"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

// LoadSkillBodies reads <dir>/skills/<slug>.md for every slug of the manifest, in order, and
// returns the raw bodies (the review engine takes the files verbatim, not skill blocks). Every
// slug is checked first (D6018: a slug is a file name, never a path), so a bad one fails before
// any file is read; each file is read with the 1 MiB cap.
func LoadSkillBodies(files Files, dir string, slugs []string) ([]string, error) {
	for _, slug := range slugs {
		if err := domain.CheckSkillSlug(slug); err != nil {
			return nil, err
		}
	}
	bodies := make([]string, len(slugs))
	for i, slug := range slugs {
		skillPath := path.Join(dir, "skills", slug+".md")
		raw, err := files.ReadFile(skillPath, domain.MaxConfigFileBytes)
		switch {
		case errors.Is(err, ErrFileTooLarge):
			return nil, domain.NewRunnerError("Skill file for slug '" + slug + "' at " + skillPath +
				" is larger than " + strconv.Itoa(domain.MaxConfigFileBytes) + " bytes")
		case err != nil:
			return nil, domain.NewRunnerError("Skill file for slug '" + slug + "' not found at " + skillPath + ": " + err.Error())
		}
		bodies[i] = string(raw)
	}
	return bodies, nil
}
