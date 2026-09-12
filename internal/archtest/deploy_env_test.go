package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestDeployWorkflowPassesEveryAnsibleEnvVar is a regression test for a real
// production incident: DEPLOY_NOTIFY_CHAT_IDS was configured correctly as a
// GitHub `production` environment variable and ansible's own .env template
// (application_deploy/tasks/main.yml) correctly read it via
// lookup('env', 'DEPLOY_NOTIFY_CHAT_IDS') — but deploy.yml's "Prepare, back
// up and deploy with Ansible" step never actually set that variable in its
// own `env:` block, so the ansible-playbook process ansible-lint couldn't
// see never saw a value to look up, and it silently landed in the deployed
// .env as empty regardless of what GitHub had configured. The failure mode
// this test exists to catch: a new lookup('env', 'X') added to the ansible
// template without a matching X: ${{ vars.X }}/${{ secrets.X }} added to
// deploy.yml's main step.
//
// This test derives the "required" var list directly from the ansible
// template itself (rather than hard-coding a list here that could itself
// drift out of sync) — every lookup('env', 'NAME') found there must appear
// as a key in deploy.yml's main deploy step's env block.
func TestDeployWorkflowPassesEveryAnsibleEnvVar(t *testing.T) {
	root := moduleRoot(t)

	ansibleTemplate, err := os.ReadFile(filepath.Join(root, "ansible/roles/application_deploy/tasks/main.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lookupPattern := regexp.MustCompile(`lookup\('env',\s*'([A-Z0-9_]+)'\)`)
	matches := lookupPattern.FindAllStringSubmatch(string(ansibleTemplate), -1)
	if len(matches) == 0 {
		t.Fatal("found no lookup('env', ...) references in application_deploy's .env template — did it move or get rewritten?")
	}
	required := make(map[string]bool, len(matches))
	for _, m := range matches {
		required[m[1]] = true
	}

	deployWorkflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/deploy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(deployWorkflow, &doc); err != nil {
		t.Fatal(err)
	}

	var deployStepEnv map[string]string
	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			if step.Run == "make deploy" {
				deployStepEnv = step.Env
			}
		}
	}
	if deployStepEnv == nil {
		t.Fatal(`could not find the "run: make deploy" step in .github/workflows/deploy.yml — did it get renamed?`)
	}

	var missing []string
	for name := range required {
		if _, ok := deployStepEnv[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("ansible's .env template reads these vars via lookup('env', ...), but deploy.yml's \"make deploy\" step never sets them — they will silently deploy as empty regardless of GitHub configuration: %v", missing)
	}
}
