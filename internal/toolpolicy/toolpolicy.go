// Package toolpolicy decides which MCP tools and resources a deployment
// exposes.
//
// The server implements every Microsoft Graph capability it has a tool for,
// but a deployment rarely wants to expose all of them at once. A tenant may be
// ready to let an assistant read calendars while mailbox access is still under
// review, or may want write operations withheld until an audit completes.
// Expressing that as a file keeps the decision in version control and in the
// deployment pipeline, rather than in a code change.
//
// Withholding happens at registration: a tool that policy excludes is never
// added to the MCP server, so it is absent from tools/list and a call naming
// it is answered with "tool not found" by the protocol layer itself. There is
// no filter for a caller to bypass and no handler left behind to reach.
package toolpolicy

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultFile is the policy path used when none is configured.
const DefaultFile = "tools.yaml"

// Mode is the starting point a selector applies its list to.
type Mode string

const (
	// ModeAll exposes every known entry except those in Exclude.
	ModeAll Mode = "all"
	// ModeNone exposes only the entries in Include.
	ModeNone Mode = "none"
)

// Selector is the policy for one kind of entry.
type Selector struct {
	// Default is "all" or "none".
	Default Mode `yaml:"default"`
	// Include names the entries to expose. Valid only with Default "none".
	Include []string `yaml:"include"`
	// Exclude names the entries to withhold. Valid only with Default "all".
	Exclude []string `yaml:"exclude"`
}

// File is the on-disk policy document.
type File struct {
	Tools     Selector `yaml:"tools"`
	Resources Selector `yaml:"resources"`
}

// Policy answers exposure questions for one deployment.
type Policy struct {
	tools     map[string]bool
	resources map[string]bool
}

// Load reads a policy file and resolves it against the names the build knows
// about.
//
// knownTools and knownResources are the complete sets the binary implements.
// They are required so that a name in the file which matches nothing can be
// rejected. A silently ignored entry is the dangerous failure here: a typo in
// an exclude list, "search_email" for "search_emails", would leave mailbox
// search exposed while the file states it is withheld, and nothing would say
// so.
//
// A missing file at the default path resolves to "expose everything", which
// preserves the behaviour of a deployment that has no policy. A missing file
// at an explicitly configured path is an error, because it means the operator
// intended a policy that is not there.
func Load(path string, explicit bool, knownTools, knownResources []string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return allowAll(knownTools, knownResources), nil
		}
		return nil, fmt.Errorf("tool policy %q: %w", path, err)
	}

	var file File
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	// Reject unrecognised fields rather than ignoring them, so a misspelled
	// key such as "excludes" fails loudly instead of exposing a tool the
	// operator believes is withheld.
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("tool policy %q: %w", path, err)
	}

	tools, err := file.Tools.resolve("tools", knownTools)
	if err != nil {
		return nil, fmt.Errorf("tool policy %q: %w", path, err)
	}
	resources, err := file.Resources.resolve("resources", knownResources)
	if err != nil {
		return nil, fmt.Errorf("tool policy %q: %w", path, err)
	}

	return &Policy{tools: tools, resources: resources}, nil
}

// AllowAll returns a policy that exposes everything. Used when no policy file
// is present.
func AllowAll(knownTools, knownResources []string) *Policy {
	return allowAll(knownTools, knownResources)
}

func allowAll(knownTools, knownResources []string) *Policy {
	p := &Policy{
		tools:     make(map[string]bool, len(knownTools)),
		resources: make(map[string]bool, len(knownResources)),
	}
	for _, name := range knownTools {
		p.tools[name] = true
	}
	for _, uri := range knownResources {
		p.resources[uri] = true
	}
	return p
}

// resolve turns one selector into the set of enabled names.
func (s Selector) resolve(kind string, known []string) (map[string]bool, error) {
	knownSet := make(map[string]bool, len(known))
	for _, name := range known {
		knownSet[name] = true
	}

	mode := s.Default
	if mode == "" {
		// An absent default is treated as "all" so that a file which only
		// lists exclusions behaves the way it reads.
		mode = ModeAll
	}

	switch mode {
	case ModeAll:
		if len(s.Include) > 0 {
			return nil, fmt.Errorf(
				"%s: include is not allowed with default %q; list what to withhold under exclude, "+
					"or set default: none and list what to expose", kind, mode)
		}
		if err := validateNames(kind, "exclude", s.Exclude, knownSet); err != nil {
			return nil, err
		}

		enabled := make(map[string]bool, len(known))
		for _, name := range known {
			enabled[name] = true
		}
		for _, name := range s.Exclude {
			delete(enabled, name)
		}
		return enabled, nil

	case ModeNone:
		if len(s.Exclude) > 0 {
			return nil, fmt.Errorf(
				"%s: exclude is not allowed with default %q; list what to expose under include, "+
					"or set default: all and list what to withhold", kind, mode)
		}
		if err := validateNames(kind, "include", s.Include, knownSet); err != nil {
			return nil, err
		}

		enabled := make(map[string]bool, len(s.Include))
		for _, name := range s.Include {
			enabled[name] = true
		}
		return enabled, nil

	default:
		return nil, fmt.Errorf("%s: default %q is not valid; use \"all\" or \"none\"", kind, mode)
	}
}

// validateNames rejects duplicates and names the build does not implement.
func validateNames(kind, field string, names []string, known map[string]bool) error {
	seen := make(map[string]bool, len(names))
	var unknown []string

	for _, name := range names {
		if name == "" {
			return fmt.Errorf("%s: %s contains an empty entry", kind, field)
		}
		if seen[name] {
			return fmt.Errorf("%s: %s lists %q twice", kind, field, name)
		}
		seen[name] = true

		if !known[name] {
			unknown = append(unknown, name)
		}
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		available := make([]string, 0, len(known))
		for name := range known {
			available = append(available, name)
		}
		sort.Strings(available)

		return fmt.Errorf("%s: %s names unknown %s: %s (available: %s)",
			kind, field, kind, strings.Join(unknown, ", "), strings.Join(available, ", "))
	}
	return nil
}

// ToolEnabled reports whether a tool is exposed.
func (p *Policy) ToolEnabled(name string) bool {
	if p == nil {
		return true
	}
	return p.tools[name]
}

// ResourceEnabled reports whether a resource is exposed.
func (p *Policy) ResourceEnabled(uri string) bool {
	if p == nil {
		return true
	}
	return p.resources[uri]
}

// EnabledTools returns the exposed tool names in sorted order.
func (p *Policy) EnabledTools() []string {
	return sortedKeys(p.tools)
}

// EnabledResources returns the exposed resource URIs in sorted order.
func (p *Policy) EnabledResources() []string {
	return sortedKeys(p.resources)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name, enabled := range set {
		if enabled {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
