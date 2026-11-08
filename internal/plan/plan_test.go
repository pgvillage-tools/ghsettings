// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgvillage-tools/ghsettings/internal/config"
	"github.com/pgvillage-tools/ghsettings/internal/github"
	"github.com/pgvillage-tools/ghsettings/internal/plan"
)

const cfgYAML = `
standard:
  settings:
    allow_auto_merge: true
    allow_merge_commit: false
  ruleset:
    required_checks: [CI result, Validate PR Title]
    merge_queue:
      merge_method: SQUASH
    rules:
      deletion: {}
      pull_request:
        required_approving_review_count: 1
        allowed_merge_methods: [squash]
orgs:
  o:
    repos:
      - match: skip-*
        ignore: true
      - match: "*"
`

const ciQueue = "on: [pull_request, merge_group]\njobs:\n  result:\n    name: CI result\n"
const titleQueue = "on: {pull_request_target: {}, merge_group: {}}\njobs:\n  v:\n    name: Validate PR Title\n"
const titleNoQueue = "on: {pull_request_target: {}}\njobs:\n  v:\n    name: Validate PR Title\n"

type fakeAPI struct {
	repos     []github.RepoSummary
	settings  map[string]map[string]any
	rulesets  map[string]map[string]any
	workflows map[string]map[string][]byte
	err       error
}

func (f *fakeAPI) ListRepos(context.Context, string) ([]github.RepoSummary, error) {
	return f.repos, f.err
}

func (f *fakeAPI) GetRepo(_ context.Context, _, repo string) (map[string]any, error) {
	return f.settings[repo], nil
}

func (f *fakeAPI) FindRuleset(_ context.Context, _, repo, _ string) (map[string]any, error) {
	return f.rulesets[repo], nil
}

func (f *fakeAPI) ListWorkflows(_ context.Context, _, repo string) (map[string][]byte, error) {
	return f.workflows[repo], nil
}

// matchingRuleset is the current ruleset that matches the desired state with a merge queue
func matchingRuleset() map[string]any {
	return map[string]any{
		"id": 42, "name": "main", "target": "branch", "enforcement": "active",
		"conditions": map[string]any{"ref_name": map[string]any{"include": []any{"~DEFAULT_BRANCH"},
			"exclude": []any{}}},
		"rules": []any{
			map[string]any{"type": "deletion"},
			map[string]any{"type": "pull_request", "parameters": map[string]any{
				"required_approving_review_count": 1, "allowed_merge_methods": []any{"squash"},
				"required_reviewers": []any{}, // not managed: ignored
			}},
			map[string]any{"type": "required_status_checks", "parameters": map[string]any{
				"strict_required_status_checks_policy": false, "do_not_enforce_on_create": false,
				"required_status_checks": []any{
					map[string]any{"context": "Validate PR Title", "integration_id": 15368},
					map[string]any{"context": "CI result", "integration_id": 15368},
				},
			}},
			map[string]any{"type": "merge_queue", "parameters": map[string]any{"merge_method": "SQUASH"}},
		},
	}
}

func changePaths(changes []plan.Change) []string {
	paths := []string{}
	for _, c := range changes {
		paths = append(paths, c.String())
	}
	return paths
}

var _ = Describe("Planner", func() {
	var (
		cfg *config.Config
		api *fakeAPI
	)

	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(cfgYAML), 0o600)).To(Succeed())
		var err error
		cfg, err = config.Load(dir)
		Expect(err).NotTo(HaveOccurred())
		api = &fakeAPI{
			repos: []github.RepoSummary{
				{Name: "good", Visibility: "public"},
				{Name: "skip-me", Visibility: "public"},
				{Name: "old", Archived: true},
			},
			settings: map[string]map[string]any{
				"good": {"allow_auto_merge": true, "allow_merge_commit": false},
			},
			rulesets: map[string]map[string]any{"good": matchingRuleset()},
			workflows: map[string]map[string][]byte{
				"good": {"ci.yml": []byte(ciQueue), "title.yml": []byte(titleQueue)},
			},
		}
	})

	planOrg := func() *plan.OrgPlan {
		p, err := (&plan.Planner{Config: cfg, API: api}).PlanOrg(context.Background(), "o")
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	It("finds no changes when the repository matches", func() {
		p := planOrg()
		Expect(p.Repos).To(HaveLen(1))
		r := p.Repos[0]
		Expect(r.Warnings).To(BeEmpty())
		Expect(r.Settings).To(BeEmpty())
		Expect(r.Ruleset.ID).To(BeEquivalentTo(42))
		Expect(r.Ruleset.MergeQueue).To(BeTrue())
		Expect(changePaths(r.Ruleset.Changes)).To(BeEmpty())
		Expect(r.HasChanges()).To(BeFalse())
		Expect(plan.HasChanges([]*plan.OrgPlan{p})).To(BeFalse())
		Expect(p.Ignored).To(Equal([]string{"skip-me"}))
		Expect(p.Archived).To(Equal([]string{"old"}))
	})

	It("shows changed settings and rule parameters", func() {
		api.settings["good"]["allow_merge_commit"] = true
		rs := matchingRuleset()
		rules := rs["rules"].([]any)
		rules[1].(map[string]any)["parameters"].(map[string]any)["required_approving_review_count"] = 0
		rules = append(rules, map[string]any{"type": "required_signatures"})
		rs["rules"] = rules
		api.rulesets["good"] = rs
		p := planOrg()
		r := p.Repos[0]
		Expect(changePaths(r.Settings)).To(Equal([]string{"~ allow_merge_commit: true -> false"}))
		Expect(changePaths(r.Ruleset.Changes)).To(Equal([]string{
			"~ rules.pull_request.required_approving_review_count: 0 -> 1",
			"- rules.required_signatures: true",
		}))
		Expect(plan.HasChanges([]*plan.OrgPlan{p})).To(BeTrue())
	})

	It("creates a missing ruleset", func() {
		delete(api.rulesets, "good")
		r := planOrg().Repos[0]
		Expect(r.Ruleset.ID).To(BeZero())
		Expect(r.HasChanges()).To(BeTrue())
		Expect(changePaths(r.Ruleset.Changes)).To(ContainElement("+ rules.deletion: true"))
	})

	It("keeps strict checks without a merge queue when a workflow lacks merge_group", func() {
		api.workflows["good"]["title.yml"] = []byte(titleNoQueue)
		r := planOrg().Repos[0]
		Expect(r.Ruleset.MergeQueue).To(BeFalse())
		Expect(r.Warnings).To(ConsistOf(ContainSubstring(`no merge_group trigger in "title.yml"`)))
		Expect(changePaths(r.Ruleset.Changes)).To(ContainElements(
			"- rules.merge_queue.merge_method: \"SQUASH\"",
			"~ rules.required_status_checks.strict_required_status_checks_policy: false -> true",
		))
	})

	It("has no merge queue on private repositories", func() {
		api.repos[0].Visibility = "private"
		r := planOrg().Repos[0]
		Expect(r.Ruleset.MergeQueue).To(BeFalse())
		Expect(r.Warnings).To(ConsistOf(ContainSubstring("not public")))
	})

	It("doesn't require checks that no workflow provides", func() {
		delete(api.workflows["good"], "ci.yml")
		r := planOrg().Repos[0]
		Expect(r.Warnings).To(ConsistOf(ContainSubstring(`check "CI result"`)))
		Expect(changePaths(r.Ruleset.Changes)).To(ContainElement(
			`~ rules.required_status_checks.required_status_checks: ["CI result (15368)","Validate PR Title (15368)"]` +
				` -> ["Validate PR Title (15368)"]`))
	})

	It("warns about settings that the API doesn't return", func() {
		delete(api.settings["good"], "allow_auto_merge")
		r := planOrg().Repos[0]
		Expect(r.Warnings).To(ConsistOf(ContainSubstring(`setting "allow_auto_merge"`)))
		Expect(changePaths(r.Settings)).To(Equal([]string{"+ allow_auto_merge: true"}))
	})

	It("lists repositories without a matching rule", func() {
		cfg.Orgs["o"] = config.Org{Repos: []config.RepoRule{{Match: "good"}}}
		api.repos = append(api.repos, github.RepoSummary{Name: "new", Visibility: "public"})
		p := planOrg()
		Expect(p.Unmanaged).To(ConsistOf("skip-me", "new"))
		Expect(p.Repos).To(HaveLen(1))
	})

	It("limits planning to a filter", func() {
		p, err := (&plan.Planner{Config: cfg, API: api, Filter: "go*"}).PlanOrg(context.Background(), "o")
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Repos).To(HaveLen(1))
		Expect(p.Ignored).To(BeEmpty())
		Expect(p.Archived).To(BeEmpty())
	})

	It("returns API errors", func() {
		api.err = errors.New("boom")
		_, err := (&plan.Planner{Config: cfg, API: api}).PlanOrg(context.Background(), "o")
		Expect(err).To(MatchError("boom"))
	})

	Describe("Render and Summary", func() {
		It("renders changes, warnings and lists", func() {
			api.workflows["good"]["title.yml"] = []byte(titleNoQueue)
			api.repos = append(api.repos, github.RepoSummary{Name: "fine", Visibility: "public"})
			api.settings["fine"] = map[string]any{"allow_auto_merge": true, "allow_merge_commit": false}
			api.rulesets["fine"] = matchingRuleset()
			api.workflows["fine"] = map[string][]byte{"ci.yml": []byte(ciQueue), "title.yml": []byte(titleQueue)}
			p := planOrg()
			var out bytes.Buffer
			Expect(plan.Render(&out, p, false)).To(Succeed())
			Expect(out.String()).To(ContainSubstring("== o/good (public)\n"))
			Expect(out.String()).To(ContainSubstring(`ruleset "main" (42): update`))
			Expect(out.String()).To(ContainSubstring("  ! no merge_group trigger"))
			Expect(out.String()).To(ContainSubstring("== o: no changes: fine\n"))
			Expect(out.String()).To(ContainSubstring("== o: ignored: skip-me\n"))
			Expect(out.String()).To(ContainSubstring("== o: archived (skipped): old\n"))
			Expect(plan.Summary([]*plan.OrgPlan{p})).To(Equal("Plan: 1 to change, 1 unchanged, 1 ignored."))
		})

		It("renders creates, settings and unchanged repos in verbose mode", func() {
			delete(api.rulesets, "good")
			api.settings["good"]["allow_auto_merge"] = false
			p := planOrg()
			var out bytes.Buffer
			Expect(plan.Render(&out, p, true)).To(Succeed())
			Expect(out.String()).To(ContainSubstring(`ruleset "main": create, with merge queue`))
			Expect(out.String()).To(ContainSubstring("  settings:\n    ~ allow_auto_merge: false -> true\n"))

			api.settings["good"]["allow_auto_merge"] = true
			api.rulesets["good"] = matchingRuleset()
			out.Reset()
			Expect(plan.Render(&out, planOrg(), true)).To(Succeed())
			Expect(out.String()).To(ContainSubstring(`ruleset "main" (42): no changes`))
		})

		It("counts unmanaged repositories", func() {
			p := &plan.OrgPlan{Unmanaged: []string{"a"}}
			Expect(plan.Summary([]*plan.OrgPlan{p})).To(Equal("Plan: 0 to change, 0 unchanged, 1 not in the config."))
			var out bytes.Buffer
			Expect(plan.Render(&out, &plan.OrgPlan{Org: "o", Unmanaged: []string{"a"}}, false)).To(Succeed())
			Expect(out.String()).To(Equal("== o: not in the config (no rule matches): a\n"))
		})
	})
})
