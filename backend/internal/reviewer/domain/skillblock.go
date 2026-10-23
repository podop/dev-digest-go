package domain

// SkillBlock renders one skill for the "## Skills / rules" section: "### <name>", then
// "_Applies when:_ <description>" when the trimmed description is not empty, a blank line and the
// trimmed body. It is the single renderer shared by reviews, eval runs and (through the review
// engine) the CI runner; reviews/domain.SkillBlock and RenderSkillBlock render the same text.
func SkillBlock(name, description, body string) string {
	return RenderSkillBlock(name, description, body)
}
