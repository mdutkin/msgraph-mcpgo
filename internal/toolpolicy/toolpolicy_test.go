package toolpolicy

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var (
	knownTools     = []string{"search_emails", "read_email", "send_email", "get_calendar_events", "list_chats"}
	knownResources = []string{"msgraph://emails", "msgraph://files", "msgraph://calendar"}
)

// write puts a policy document in a temporary directory and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tools.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func load(t *testing.T, body string) (*Policy, error) {
	t.Helper()
	return Load(write(t, body), true, knownTools, knownResources)
}

func mustLoad(t *testing.T, body string) *Policy {
	t.Helper()
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

// The motivating case: the build can read mail, but this deployment does not
// expose it yet.
func TestDefaultAllWithExclusions(t *testing.T) {
	p := mustLoad(t, `
tools:
  default: all
  exclude:
    - search_emails
    - read_email
    - send_email
resources:
  default: all
  exclude:
    - msgraph://emails
`)

	for _, withheld := range []string{"search_emails", "read_email", "send_email"} {
		if p.ToolEnabled(withheld) {
			t.Errorf("tool %q should be withheld", withheld)
		}
	}
	for _, exposed := range []string{"get_calendar_events", "list_chats"} {
		if !p.ToolEnabled(exposed) {
			t.Errorf("tool %q should be exposed", exposed)
		}
	}
	if p.ResourceEnabled("msgraph://emails") {
		t.Error("the emails resource should be withheld")
	}
	if !p.ResourceEnabled("msgraph://calendar") {
		t.Error("the calendar resource should be exposed")
	}
}

func TestDefaultNoneWithInclusions(t *testing.T) {
	p := mustLoad(t, `
tools:
  default: none
  include:
    - get_calendar_events
resources:
  default: none
  include:
    - msgraph://calendar
`)

	if want := []string{"get_calendar_events"}; !reflect.DeepEqual(p.EnabledTools(), want) {
		t.Errorf("enabled tools = %v, want %v", p.EnabledTools(), want)
	}
	if want := []string{"msgraph://calendar"}; !reflect.DeepEqual(p.EnabledResources(), want) {
		t.Errorf("enabled resources = %v, want %v", p.EnabledResources(), want)
	}
	if p.ToolEnabled("search_emails") {
		t.Error("an unlisted tool should be withheld under default: none")
	}
}

// A file that only lists exclusions should read the way it behaves.
func TestAbsentDefaultMeansAll(t *testing.T) {
	p := mustLoad(t, `
tools:
  exclude:
    - send_email
`)

	if p.ToolEnabled("send_email") {
		t.Error("send_email should be withheld")
	}
	if !p.ToolEnabled("search_emails") {
		t.Error("unlisted tools should stay exposed")
	}
}

// An empty document exposes everything, which is what "no policy" means.
func TestEmptyDocumentExposesEverything(t *testing.T) {
	p := mustLoad(t, "{}\n")

	if len(p.EnabledTools()) != len(knownTools) {
		t.Fatalf("expected all %d tools, got %d", len(knownTools), len(p.EnabledTools()))
	}
}

// The dangerous failure: a typo in an exclude list would leave the tool
// exposed while the file states it is withheld.
func TestUnknownNameIsRejected(t *testing.T) {
	_, err := load(t, `
tools:
  default: all
  exclude:
    - search_email
`)
	if err == nil {
		t.Fatal("a misspelled tool name was accepted")
	}
	if !strings.Contains(err.Error(), "search_email") {
		t.Errorf("error does not name the offending entry: %v", err)
	}
	if !strings.Contains(err.Error(), "available:") {
		t.Errorf("error does not list the valid names: %v", err)
	}
}

// A misspelled key must not be silently ignored.
func TestUnknownFieldIsRejected(t *testing.T) {
	if _, err := load(t, "tools:\n  default: all\n  excludes:\n    - send_email\n"); err == nil {
		t.Fatal("the misspelled key \"excludes\" was accepted")
	}
}

func TestContradictoryCombinationsAreRejected(t *testing.T) {
	tests := map[string]string{
		"include with default all": "tools:\n  default: all\n  include:\n    - send_email\n",
		"exclude with default none": "tools:\n  default: none\n  include:\n    - send_email\n  " +
			"exclude:\n    - read_email\n",
		"invalid default": "tools:\n  default: some\n",
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, body); err == nil {
				t.Fatal("contradictory policy was accepted")
			}
		})
	}
}

func TestDuplicateAndEmptyEntriesAreRejected(t *testing.T) {
	if _, err := load(t, "tools:\n  default: all\n  exclude:\n    - send_email\n    - send_email\n"); err == nil {
		t.Error("a duplicated entry was accepted")
	}
	if _, err := load(t, "tools:\n  default: all\n  exclude:\n    - \"\"\n"); err == nil {
		t.Error("an empty entry was accepted")
	}
}

// A missing file at the default path means "no policy", which exposes
// everything. A missing file at a configured path means the operator intended
// a policy that is not there.
func TestMissingFileBehaviour(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yaml")

	p, err := Load(absent, false, knownTools, knownResources)
	if err != nil {
		t.Fatalf("an implicit missing file should not be an error: %v", err)
	}
	if len(p.EnabledTools()) != len(knownTools) {
		t.Error("an implicit missing file should expose everything")
	}

	if _, err := Load(absent, true, knownTools, knownResources); err == nil {
		t.Fatal("an explicitly configured missing file should be an error")
	}
}

func TestMalformedYAMLIsRejected(t *testing.T) {
	if _, err := load(t, "tools: [this is not a mapping\n"); err == nil {
		t.Fatal("malformed YAML was accepted")
	}
}

// A nil Policy must not withhold anything, so callers need no nil check.
func TestNilPolicyExposesEverything(t *testing.T) {
	var p *Policy
	if !p.ToolEnabled("search_emails") || !p.ResourceEnabled("msgraph://emails") {
		t.Fatal("a nil policy withheld an entry")
	}
}

func TestAllowAll(t *testing.T) {
	p := AllowAll(knownTools, knownResources)
	if len(p.EnabledTools()) != len(knownTools) || len(p.EnabledResources()) != len(knownResources) {
		t.Fatal("AllowAll did not expose everything")
	}
}
