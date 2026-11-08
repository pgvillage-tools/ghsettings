// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgvillage-tools/ghsettings/internal/config"
	"github.com/pgvillage-tools/ghsettings/internal/github"
	"github.com/pgvillage-tools/ghsettings/internal/plan"
)

func settings(autoMerge bool) map[string]any {
	return map[string]any{
		"allow_auto_merge": autoMerge, "allow_merge_commit": true, "allow_rebase_merge": true,
		"allow_squash_merge": true, "allow_update_branch": false, "delete_branch_on_merge": true,
	}
}

// strictRuleset is a ruleset without merge queue, with strict checks, and with fields the API returns but the config
// doesn't need
func strictRuleset(id int, checks ...string) map[string]any {
	required := []any{}
	for _, c := range checks {
		required = append(required, map[string]any{"context": c, "integration_id": 15368})
	}
	return map[string]any{
		"id": id, "name": "main", "target": "branch", "enforcement": "active", "source_type": "Repository",
		"conditions": map[string]any{"ref_name": map[string]any{"include": []any{"~DEFAULT_BRANCH"},
			"exclude": []any{}}},
		"rules": []any{
			map[string]any{"type": "deletion"},
			map[string]any{"type": "pull_request", "parameters": map[string]any{
				"required_approving_review_count": 1, "allowed_merge_methods": []any{"merge", "rebase", "squash"},
				"required_reviewers":    []any{},
				"dismissal_restriction": map[string]any{"enabled": false, "allowed_actors": []any{}},
			}},
			map[string]any{"type": "required_status_checks", "parameters": map[string]any{
				"strict_required_status_checks_policy": true, "do_not_enforce_on_create": false,
				"required_status_checks": required,
			}},
		},
	}
}

const lintWorkflow = "on: [pull_request]\njobs:\n  lint:\n    name: Ansible Lint\n"

var _ = Describe("Exporter", func() {
	var api *fakeAPI

	BeforeEach(func() {
		api = &fakeAPI{
			repos: []github.RepoSummary{
				{Name: "role-a", Visibility: "public"},
				{Name: "role-b", Visibility: "public"},
				{Name: "queued", Visibility: "public"},
				{Name: "bare", Visibility: "private"},
				{Name: "gone", Archived: true},
			},
			settings: map[string]map[string]any{
				"role-a": settings(true), "role-b": settings(true), "queued": settings(true), "bare": settings(false),
			},
			rulesets: map[string]map[string]any{
				"role-a": strictRuleset(1, "Ansible Lint"),
				"role-b": strictRuleset(2, "Ansible Lint"),
				"queued": matchingRuleset(),
			},
			workflows: map[string]map[string][]byte{
				"role-a": {"lint.yml": []byte(lintWorkflow)},
				"role-b": {"lint.yml": []byte(lintWorkflow)},
				"queued": {"ci.yml": []byte(ciQueue), "title.yml": []byte(titleQueue)},
			},
		}
	})

	export := func() (*config.Config, []string) {
		cfg, warnings, err := (&plan.Exporter{API: api}).Export(context.Background(), "o")
		Expect(err).NotTo(HaveOccurred())
		return cfg, warnings
	}

	It("exports a config that plans without changes, after a round trip through YAML", func() {
		cfg, warnings := export()
		Expect(warnings).To(BeEmpty())
		data, err := yaml.Marshal(cfg)
		Expect(err).NotTo(HaveOccurred())
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "o.yaml"), data, 0o600)).To(Succeed())
		loaded, err := config.Load(dir)
		Expect(err).NotTo(HaveOccurred())

		p, err := (&plan.Planner{Config: loaded, API: api}).PlanOrg(context.Background(), "o")
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Repos).To(HaveLen(4))
		for _, r := range p.Repos {
			Expect(r.HasChanges()).To(BeFalse(), "%s: %v %v", r.Repo, r.Settings, r.Ruleset)
		}
		Expect(p.Unmanaged).To(BeEmpty())
		Expect(p.Archived).To(Equal([]string{"gone"}))
	})

	It("groups repositories with the same state in a profile", func() {
		cfg, _ := export()
		Expect(cfg.Profiles).To(HaveKey("role"))
		Expect(cfg.Orgs["o"].Repos).To(ConsistOf(
			config.RepoRule{Match: "bare", Layer: config.Layer{Settings: settings(false)}},
			HaveField("Match", "queued"),
			config.RepoRule{Match: "role-*", Profile: "role"},
		))
	})

	It("leaves out defaults and empty parameters", func() {
		cfg, _ := export()
		rs := cfg.Profiles["role"].Ruleset
		Expect(rs).NotTo(HaveKey("name"))
		Expect(rs).NotTo(HaveKey("conditions"))
		Expect(rs).NotTo(HaveKey("merge_queue"))
		Expect(rs["required_checks"]).To(Equal([]any{"Ansible Lint"}))
		rules := rs["rules"].(map[string]any)
		Expect(rules["deletion"]).To(BeEmpty())
		Expect(rules["pull_request"]).NotTo(HaveKey("required_reviewers"))
		Expect(rules["pull_request"]).NotTo(HaveKey("dismissal_restriction"))
	})

	It("names groups without a common prefix, and lists their repositories", func() {
		api.repos = append(api.repos, github.RepoSummary{Name: "other", Visibility: "public"})
		api.settings["other"] = settings(true)
		api.rulesets["other"] = strictRuleset(3, "Ansible Lint")
		api.workflows["other"] = map[string][]byte{"lint.yml": []byte(lintWorkflow)}
		cfg, _ := export()
		Expect(cfg.Profiles).To(HaveLen(1))
		var profile string
		for name := range cfg.Profiles {
			profile = name
		}
		Expect(profile).To(HavePrefix("group-"))
		Expect(cfg.Orgs["o"].Repos).To(ContainElements(
			config.RepoRule{Match: "other", Profile: profile},
			config.RepoRule{Match: "role-a", Profile: profile},
		))
	})

	It("warns about state that the config can't express", func() {
		rs := strictRuleset(1, "Ansible Lint")
		rules := rs["rules"].([]any)
		params := rules[2].(map[string]any)["parameters"].(map[string]any)
		params["strict_required_status_checks_policy"] = false
		params["required_status_checks"] = []any{
			map[string]any{"context": "a", "integration_id": 15368},
			map[string]any{"context": "b", "integration_id": 1},
		}
		api.rulesets["role-a"] = rs
		delete(api.settings["bare"], "allow_update_branch")
		_, warnings := export()
		Expect(warnings).To(ConsistOf(
			ContainSubstring(`o/bare: setting "allow_update_branch"`),
			ContainSubstring("o/role-a: required checks come from several apps"),
			ContainSubstring("o/role-a: strict required checks is false with merge queue false"),
		))
	})

	It("exports checks of another app and other ruleset fields", func() {
		rs := strictRuleset(1, "external")
		rs["enforcement"] = "evaluate"
		rs["conditions"] = map[string]any{"ref_name": map[string]any{"include": []any{"refs/heads/main"},
			"exclude": []any{}}}
		rules := rs["rules"].([]any)
		params := rules[2].(map[string]any)["parameters"].(map[string]any)
		params["required_status_checks"] = []any{map[string]any{"context": "external", "integration_id": 99}}
		api.rulesets["role-a"] = rs
		cfg, _ := export()
		layer := cfg.Orgs["o"].Repos[2].Layer
		Expect(cfg.Orgs["o"].Repos[2].Match).To(Equal("role-a"))
		Expect(layer.Ruleset).To(HaveKeyWithValue("checks_integration_id", 99))
		Expect(layer.Ruleset).To(HaveKeyWithValue("enforcement", "evaluate"))
		Expect(layer.Ruleset).To(HaveKey("conditions"))
	})

	It("limits the export to a filter, and selects settings", func() {
		cfg, _, err := (&plan.Exporter{API: api, Filter: "role-a", Settings: []string{"allow_auto_merge"}}).
			Export(context.Background(), "o")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Orgs["o"].Repos).To(HaveLen(1))
		Expect(cfg.Orgs["o"].Repos[0].Layer.Settings).To(Equal(map[string]any{"allow_auto_merge": true}))
	})

	It("returns API errors", func() {
		api.err = context.Canceled
		_, _, err := (&plan.Exporter{API: api}).Export(context.Background(), "o")
		Expect(err).To(MatchError(context.Canceled))
	})
})
