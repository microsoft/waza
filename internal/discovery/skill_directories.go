package discovery

// SkillDirectories returns the workspace directories selected by Discover.
// Discovery only reads local project/skill files and never starts a service.
func SkillDirectories(root string) ([]string, error) {
	skills, err := Discover(root)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(skills))
	for _, skill := range skills {
		dirs = append(dirs, skill.Dir)
	}
	return dirs, nil
}
