package selfupdate

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// buildExprRE matches a GitHub Actions expression: ${{ … }}.
var buildExprRE = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

// TestReleaseWorkflowNotesBodyHasNoShellSubstitution guards a release-notes bug
// that shipped in v0.0.59 and v0.0.60.
//
// The `body:` of the GitHub Release step is a YAML block scalar that GitHub
// passes to the action as a plain input string. Only ${{ … }} is substituted
// in it — there is no shell — so a bare `$(date …)` written there reaches the
// published notes verbatim:
//
//	Built: $(date -u +%Y-%m-%dT%H:%M:%SZ)
//
// The guard is deliberately narrow (the release body only): command
// substitution is the correct thing inside a step's `run:` script, and that is
// where every other `$(…)` in the workflows lives.
func TestReleaseWorkflowNotesBodyHasNoShellSubstitution(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}

	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string         `yaml:"name"`
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}

	bodies := 0
	for jobName, job := range doc.Jobs {
		for _, step := range job.Steps {
			if !strings.Contains(step.Uses, "action-gh-release") {
				continue
			}
			body, ok := step.With["body"].(string)
			if !ok {
				t.Fatalf("job %q step %q: release step has no string body", jobName, step.Name)
			}
			bodies++

			// What the published notes look like: only expressions are
			// substituted, everything else is taken literally.
			rendered := buildExprRE.ReplaceAllString(body, "<expr>")
			if strings.Contains(rendered, "$(") {
				for _, line := range strings.Split(rendered, "\n") {
					if strings.Contains(line, "$(") {
						t.Errorf("release notes would ship shell substitution verbatim on:\n\t%s\nshell command substitution is never expanded in a `body:` block scalar — resolve it in a `run:` step and interpolate the value with ${{ }}", strings.TrimSpace(line))
					}
				}
			}
		}
	}

	if bodies == 0 {
		t.Fatal("found no action-gh-release step with a body — this guard silently stopped guarding; update it if the release step was intentionally replaced")
	}
}
