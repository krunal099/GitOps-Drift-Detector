package config

import (
	"os"

	"sigs.k8s.io/yaml"
)

// Config is the top-level configuration for the drift detector.
// It can be loaded from a YAML file or composed from CLI flags.
type Config struct {
	// ManifestsPath is the directory containing the desired-state YAML files (your "Git source of truth").
	ManifestsPath string `yaml:"manifestsPath"`

	// Namespace restricts watching to a single namespace. Empty string = all namespaces.
	Namespace string `yaml:"namespace"`

	// IntervalSeconds controls how often the reconciliation loop runs.
	IntervalSeconds int `yaml:"intervalSeconds"`

	// Remediate enables auto-applying the desired state back to the cluster when drift is detected.
	// This is always overridden to false when DryRun is true.
	Remediate bool `yaml:"remediate"`

	// DryRun reports drift but never applies any changes.
	DryRun bool `yaml:"dryRun"`

	// GlobalIgnoreFields is a list of dot-path field expressions that are always excluded from
	// drift comparison across all resources. Example: ["spec.replicas"]
	GlobalIgnoreFields []string `yaml:"globalIgnoreFields"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.IntervalSeconds == 0 {
		cfg.IntervalSeconds = 60
	}
	return &cfg, nil
}
