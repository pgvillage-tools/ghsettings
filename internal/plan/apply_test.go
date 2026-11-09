// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgvillage-tools/ghsettings/internal/config"
	"github.com/pgvillage-tools/ghsettings/internal/github"
	"github.com/pgvillage-tools/ghsettings/internal/plan"
)

// writableAPI is a fake GitHub that keeps the changes, so a plan after apply sees them.
type writableAPI struct {
	*fakeAPI
	calls    []string
	puts     []map[string]any
	writeErr error
}

func toMap(v any) map[string]any {
	data, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	var m map[string]any
	Expect(json.Unmarshal(data, &m)).To(Succeed())
	return m
}

func (w *writableAPI) UpdateRepo(_ context.Context, _, repo string, fields map[string]any) error {
	w.calls = append(w.calls, "PATCH "+repo)
	if w.writeErr != nil {
		return w.writeErr
	}
	if w.settings[repo] == nil {
		w.settings[repo] = map[string]any{}
	}
	for k, v := range fields {
		w.settings[repo][k] = v
	}
	return nil
}

func (w *writableAPI) CreateRuleset(_ context.Context, _, repo string, ruleset any) (int64, error) {
	w.calls = append(w.calls, "POST "+repo)
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	rs := toMap(ruleset)
	rs["id"] = 100
	w.rulesets[repo] = rs
	return 100, nil
}

func (w *writableAPI) UpdateRuleset(_ context.Context, _, repo string, id int64, ruleset any) error {
	w.calls = append(w.calls, "PUT "+repo)
	if w.writeErr != nil {
		return w.writeErr
	}
	body := toMap(ruleset)
	w.puts = append(w.puts, body)
	rs := w.rulesets[repo]
	for k, v := range body { // fields left out are kept, like bypass_actors
		rs[k] = v
	}
	rs["id"] = id
	return nil
}

var _ = Describe("Apply", func() {
	var (
		cfg *config.Config
		api *writableAPI
	)

	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(cfgYAML), 0o600)).To(Succeed())
		var err error
		cfg, err = config.Load(dir)
		Expect(err).NotTo(HaveOccurred())
		current := matchingRuleset()
		current["bypass_actors"] = []any{map[string]any{"actor_id": 5, "actor_type": "RepositoryRole"}}
		api = &writableAPI{fakeAPI: &fakeAPI{
			repos: []github.RepoSummary{
				{Name: "good", Visibility: "public"},
				{Name: "drift", Visibility: "public"},
				{Name: "new", Visibility: "public"},
			},
			settings: map[string]map[string]any{
				"good":  {"allow_auto_merge": true, "allow_merge_commit": false},
				"drift": {"allow_auto_merge": false, "allow_merge_commit": false},
				"new":   {"allow_auto_merge": true, "allow_merge_commit": false},
			},
			rulesets: map[string]map[string]any{"good": matchingRuleset(), "drift": current},
			workflows: map[string]map[string][]byte{
				"good":  {"ci.yml": []byte(ciQueue), "title.yml": []byte(titleQueue)},
				"drift": {"ci.yml": []byte(ciQueue), "title.yml": []byte(titleQueue)},
				"new":   {"ci.yml": []byte(ciQueue), "title.yml": []byte(titleQueue)},
			},
		}}
		// drift: a changed parameter, an unmanaged parameter, and a rule that is not desired
		rules := current["rules"].([]any)
		pr := rules[1].(map[string]any)["parameters"].(map[string]any)
		pr["required_approving_review_count"] = 0
		pr["required_reviewers"] = []any{map[string]any{"reviewer": "x"}}
		current["rules"] = append(rules, map[string]any{"type": "required_signatures"})
	})

	planOrg := func() *plan.OrgPlan {
		p, err := (&plan.Planner{Config: cfg, API: api}).PlanOrg(context.Background(), "o")
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	It("changes only what the plan shows, after which the plan is empty", func() {
		var out bytes.Buffer
		Expect(plan.Apply(context.Background(), api, planOrg(), &out)).To(Succeed())
		Expect(api.calls).To(Equal([]string{"PATCH drift", "PUT drift", "POST new"}))
		Expect(out.String()).To(Equal("o/drift: settings updated (1)\n" +
			"o/drift: ruleset \"main\" updated (42)\n" +
			"o/new: ruleset \"main\" created (100)\n"))
		Expect(plan.HasChanges([]*plan.OrgPlan{planOrg()})).To(BeFalse())
	})

	It("keeps bypass actors and unmanaged parameters, and removes rules that are not desired", func() {
		Expect(plan.Apply(context.Background(), api, planOrg(), &bytes.Buffer{})).To(Succeed())
		Expect(api.puts).To(HaveLen(1))
		Expect(api.puts[0]).NotTo(HaveKey("bypass_actors"))
		Expect(api.rulesets["drift"]).To(HaveKey("bypass_actors"))
		types := []string{}
		for _, r := range api.puts[0]["rules"].([]any) {
			rule := r.(map[string]any)
			types = append(types, rule["type"].(string))
			if rule["type"] == "pull_request" {
				params := rule["parameters"].(map[string]any)
				Expect(params).To(HaveKeyWithValue("required_approving_review_count", BeEquivalentTo(1)))
				Expect(params).To(HaveKey("required_reviewers"))
			}
		}
		Expect(types).To(Equal([]string{"deletion", "pull_request", "required_status_checks", "merge_queue"}))
	})

	It("doesn't touch anything without changes", func() {
		api.repos = api.repos[:1]
		Expect(plan.Apply(context.Background(), api, planOrg(), &bytes.Buffer{})).To(Succeed())
		Expect(api.calls).To(BeEmpty())
	})

	DescribeTable("stops at the first error",
		func(repo, msg string) {
			api.repos = []github.RepoSummary{{Name: repo, Visibility: "public"}}
			if repo == "settings-only" {
				api.settings[repo] = map[string]any{"allow_auto_merge": false, "allow_merge_commit": false}
				api.rulesets[repo] = matchingRuleset()
				api.workflows[repo] = api.workflows["good"]
			}
			api.writeErr = errors.New("denied")
			err := plan.Apply(context.Background(), api, planOrg(), &bytes.Buffer{})
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(api.calls).To(HaveLen(1))
		},
		Entry("settings", "settings-only", "o/settings-only: updating settings: denied"),
		Entry("create", "new", `o/new: creating ruleset "main": denied`),
	)

	It("returns errors of updates", func() {
		api.repos = []github.RepoSummary{{Name: "drift", Visibility: "public"}}
		api.settings["drift"]["allow_auto_merge"] = true
		api.writeErr = errors.New("denied")
		err := plan.Apply(context.Background(), api, planOrg(), &bytes.Buffer{})
		Expect(err).To(MatchError(ContainSubstring(`o/drift: updating ruleset "main": denied`)))
	})
})
