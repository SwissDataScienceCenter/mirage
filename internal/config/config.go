// Package config loads and validates Mirage's configuration file.
//
// The file names the Confined Resources — those Mirage confines to the Target
// Namespace — and the Masked Resources — those Mirage answers itself. Anything not
// named is Passed Through. See CONTEXT.md for the vocabulary.
package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

// Resource identifies a resource kind independently of its API version. An entry
// applies to every version of that resource, so a Client reading both v1alpha1 and
// v1beta1 needs one entry rather than two.
type Resource struct {
	// Group is the API group, empty for the core group.
	Group string `yaml:"group"`
	// Resource is the plural name as it appears in the URL, e.g. "taskruns".
	Resource string `yaml:"resource"`
}

func (r Resource) String() string {
	if r.Group == "" {
		return r.Resource
	}
	return r.Resource + "." + r.Group
}

// Masked is a Resource that Mirage answers itself rather than forwarding.
type Masked struct {
	Resource `yaml:",inline"`
	// Kind names the object kind, so Mirage can name the empty list it synthesises
	// ("ClusterBuildStrategy" yields "ClusterBuildStrategyList"). Required.
	Kind string `yaml:"kind"`
}

// Config is Mirage's on-disk configuration.
type Config struct {
	// Confined names the resources whose cluster-wide requests are confined to the
	// Target Namespace.
	Confined []Resource `yaml:"confined"`
	// Masked names the resources Mirage answers itself, as existing but empty.
	Masked []Masked `yaml:"masked"`
}

// Load reads and validates the configuration file at path.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return c, nil
}

// Validate reports whether the configuration is usable.
//
// A resource may not be both confined and masked: the two decisions are mutually
// exclusive, and silently preferring one would make Mirage's behaviour depend on
// an ordering the Deployer cannot see.
func (c Config) Validate() error {
	seen := make(map[Resource]string, len(c.Confined)+len(c.Masked))

	for _, r := range c.Confined {
		if r.Resource == "" {
			return fmt.Errorf("confined entry with group %q has no resource", r.Group)
		}
		// Confining means inserting the Target Namespace into the path, which is
		// meaningless for a cluster-scoped resource. Namespaces is the one case
		// Mirage can recognise on its own, and the one a Deployer is most likely to
		// reach for, so it is refused with a reason rather than accepted and
		// rewritten into a path the API server does not serve. Masking it is a
		// coherent choice and remains allowed.
		if r.Group == "" && r.Resource == "namespaces" {
			return fmt.Errorf("namespaces cannot be confined: it is cluster-scoped, so there is no namespaced path to rewrite it into; mask it instead if the Client should see none")
		}
		if where, dup := seen[r]; dup {
			return fmt.Errorf("resource %s appears in both confined and %s", r, where)
		}
		seen[r] = "confined"
	}

	for _, m := range c.Masked {
		if m.Resource.Resource == "" {
			return fmt.Errorf("masked entry with group %q has no resource", m.Group)
		}
		if m.Kind == "" {
			return fmt.Errorf("masked entry %s has no kind; it is required so Mirage can name the empty list it synthesises", m.Resource)
		}
		if where, dup := seen[m.Resource]; dup {
			return fmt.Errorf("resource %s appears in both masked and %s", m.Resource, where)
		}
		seen[m.Resource] = "masked"
	}

	return nil
}

// LogValue renders the resolved configuration for the startup log line. Mirage
// echoes back everything it loaded so the first lines of its logs say what it
// actually believes, rather than what the Deployer intended.
func (c Config) LogValue() slog.Value {
	confined := make([]string, 0, len(c.Confined))
	for _, r := range c.Confined {
		confined = append(confined, r.String())
	}
	masked := make([]string, 0, len(c.Masked))
	for _, m := range c.Masked {
		masked = append(masked, m.String()+" as "+m.Kind)
	}
	return slog.GroupValue(
		slog.Any("confined", confined),
		slog.Any("masked", masked),
	)
}
