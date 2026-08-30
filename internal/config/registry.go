package config

// DefaultPublicRegistryURL is the built-in public registry index source.
const DefaultPublicRegistryURL = "https://github.com/waza-evals"

// RegistrySource describes one configured registry index source used for
// discovery. Resolver/package content remains canonical outside the index.
type RegistrySource struct {
	Name     string `yaml:"name" json:"name"`
	URL      string `yaml:"url" json:"url"`
	Priority int    `yaml:"priority,omitempty" json:"priority,omitempty"`
}

// DefaultRegistrySources returns the built-in public registry sources.
func DefaultRegistrySources() []RegistrySource {
	return []RegistrySource{
		{
			Name: "public",
			URL:  DefaultPublicRegistryURL,
		},
	}
}
