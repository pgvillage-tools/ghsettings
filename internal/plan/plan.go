// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

// Package plan compares the desired state of repositories with their current state on GitHub.
package plan

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/pgvillage-tools/ghsettings/internal/config"
	"github.com/pgvillage-tools/ghsettings/internal/github"
	"github.com/pgvillage-tools/ghsettings/internal/workflows"
)

// API is the part of the GitHub API that planning needs.
type API interface {
	ListRepos(ctx context.Context, org string) ([]github.RepoSummary, error)
	GetRepo(ctx context.Context, owner, repo string) (map[string]any, error)
	FindRuleset(ctx context.Context, owner, repo, name string) (map[string]any, error)
	ListWorkflows(ctx context.Context, owner, repo string) (map[string][]byte, error)
}

// RulesetPlan is the plan for the ruleset of a repository.
type RulesetPlan struct {
	Name string
	// ID of the existing ruleset; 0 when it must be created.
	ID int64
	// Body is the desired ruleset, as it would be sent to the API.
	Body       *Ruleset
	MergeQueue bool
	Changes    []Change
}

// RepoPlan is the plan for one repository.
type RepoPlan struct {
	Org        string
	Repo       string
	Visibility string
	Resolution config.Resolution
	Settings   []Change
	Ruleset    *RulesetPlan
	Warnings   []string
}

// HasChanges reports whether anything must change.
func (p RepoPlan) HasChanges() bool {
	return len(p.Settings) > 0 || (p.Ruleset != nil && (p.Ruleset.ID == 0 || len(p.Ruleset.Changes) > 0))
}

// OrgPlan is the plan for all repositories of an org.
type OrgPlan struct {
	Org string
	// Repos are the managed repositories.
	Repos     []RepoPlan
	Ignored   []string
	Unmanaged []string
	Archived  []string
}

// Planner builds plans.
type Planner struct {
	Config *config.Config
	API    API
	// Filter limits planning to the repositories matching this glob; empty for all.
	Filter string
}

// PlanOrg plans the repositories of an org (those matching Filter, when set).
func (p *Planner) PlanOrg(ctx context.Context, org string) (*OrgPlan, error) {
	repos, err := p.API.ListRepos(ctx, org)
	if err != nil {
		return nil, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	plan := &OrgPlan{Org: org}
	for _, r := range repos {
		if matched, _ := path.Match(p.Filter, r.Name); p.Filter != "" && !matched {
			continue
		}
		if r.Archived {
			plan.Archived = append(plan.Archived, r.Name)
			continue
		}
		desired, resolution, resolveErr := p.Config.Resolve(org, r.Name)
		if resolveErr != nil {
			return nil, resolveErr
		}
		switch resolution {
		case config.Ignored:
			plan.Ignored = append(plan.Ignored, r.Name)
			continue
		case config.Unmanaged:
			plan.Unmanaged = append(plan.Unmanaged, r.Name)
			continue
		}
		repoPlan, planErr := p.planRepo(ctx, org, r, desired)
		if planErr != nil {
			return nil, planErr
		}
		plan.Repos = append(plan.Repos, *repoPlan)
	}
	return plan, nil
}

func (p *Planner) planRepo(ctx context.Context, org string, r github.RepoSummary,
	desired *config.Desired) (*RepoPlan, error) {
	plan := &RepoPlan{Org: org, Repo: r.Name, Visibility: r.Visibility, Resolution: config.Managed}
	if len(desired.Settings) > 0 {
		current, err := p.API.GetRepo(ctx, org, r.Name)
		if err != nil {
			return nil, err
		}
		for k := range desired.Settings {
			if _, ok := current[k]; !ok {
				plan.Warnings = append(plan.Warnings,
					fmt.Sprintf("setting %q is not in the API response (unknown field, or no access to it)", k))
			}
		}
		plan.Settings = diffMaps(pick(current, desired.Settings), desired.Settings)
	}
	if desired.Ruleset == nil {
		return plan, nil
	}
	files, err := p.API.ListWorkflows(ctx, org, r.Name)
	if err != nil {
		return nil, err
	}
	wfs, err := workflows.ParseAll(files)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", org, r.Name, err)
	}
	checks, mergeQueue, warnings := decideChecks(desired.Ruleset, wfs, r.Visibility == "public")
	plan.Warnings = append(plan.Warnings, warnings...)
	body := rulesetBody(desired.Ruleset, checks, mergeQueue)
	rsPlan := &RulesetPlan{Name: desired.Ruleset.Name, Body: body, MergeQueue: mergeQueue}
	raw, err := p.API.FindRuleset(ctx, org, r.Name, desired.Ruleset.Name)
	if err != nil {
		return nil, err
	}
	managed := managedParams(body)
	currentView := map[string]any{}
	if raw != nil {
		current, decodeErr := decodeRuleset(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("%s/%s: ruleset %q: %w", org, r.Name, desired.Ruleset.Name, decodeErr)
		}
		rsPlan.ID = current.ID
		currentView = rulesetView(current, managed)
	}
	rsPlan.Changes = diffMaps(currentView, rulesetView(body, managed))
	plan.Ruleset = rsPlan
	return plan, nil
}

// pick returns the fields of current that are in keys.
func pick(current, keys map[string]any) map[string]any {
	out := map[string]any{}
	for k := range keys {
		if v, ok := current[k]; ok {
			out[k] = v
		}
	}
	return out
}

// decideChecks returns the required checks that a workflow provides, and whether the merge queue can be enabled:
// only on a public repository where every configured check is provided by a workflow with a merge_group trigger.
// Without it, queued pull requests would wait for checks that never come, or merge without the checks.
func decideChecks(rs *config.Ruleset, wfs workflows.Set, public bool) (checks []string, mergeQueue bool,
	warnings []string) {
	var noMergeGroup, missing []string
	for _, check := range rs.RequiredChecks {
		wf := wfs.Find(check)
		if wf == nil {
			warnings = append(warnings, fmt.Sprintf("no workflow job provides check %q; not requiring it", check))
			missing = append(missing, check)
			continue
		}
		checks = append(checks, check)
		if !wf.MergeGroup && !contains(noMergeGroup, wf.Path) {
			noMergeGroup = append(noMergeGroup, wf.Path)
		}
	}
	if rs.MergeQueue == nil {
		return checks, false, warnings
	}
	switch {
	case !public:
		warnings = append(warnings, "not public: no merge queue (private repositories need GitHub Enterprise "+
			"for it); requiring up-to-date branches instead")
	case len(missing) > 0:
		warnings = append(warnings, fmt.Sprintf("checks %s not provided: no merge queue yet; requiring "+
			"up-to-date branches instead", quoteList(missing)))
	case len(noMergeGroup) > 0:
		warnings = append(warnings, fmt.Sprintf("no merge_group trigger in %s: no merge queue yet; requiring "+
			"up-to-date branches instead", quoteList(noMergeGroup)))
	default:
		mergeQueue = true
	}
	return checks, mergeQueue, warnings
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// rulesetBody builds the ruleset as the API expects it.
func rulesetBody(rs *config.Ruleset, checks []string, mergeQueue bool) *Ruleset {
	types := make([]string, 0, len(rs.Rules))
	for t := range rs.Rules {
		types = append(types, t)
	}
	sort.Strings(types)
	body := &Ruleset{Name: rs.Name, Target: rs.Target, Enforcement: rs.Enforcement, Conditions: rs.Conditions}
	for _, t := range types {
		body.Rules = append(body.Rules, Rule{Type: t, Parameters: rs.Rules[t]})
	}
	if len(checks) > 0 {
		required := make([]any, 0, len(checks))
		for _, c := range checks {
			required = append(required, map[string]any{"context": c, "integration_id": rs.ChecksIntegrationID})
		}
		body.Rules = append(body.Rules, Rule{Type: "required_status_checks", Parameters: map[string]any{
			// The merge queue tests pull requests on top of the latest default branch, which replaces this
			"strict_required_status_checks_policy": !mergeQueue,
			"do_not_enforce_on_create":             false,
			"required_status_checks":               required,
		}})
	}
	if mergeQueue {
		body.Rules = append(body.Rules, Rule{Type: "merge_queue", Parameters: rs.MergeQueue})
	}
	return body
}

// Summary counts the repositories of plans.
func Summary(plans []*OrgPlan) string {
	changed, unchanged, ignored, unmanaged := 0, 0, 0, 0
	for _, p := range plans {
		for _, r := range p.Repos {
			if r.HasChanges() {
				changed++
			} else {
				unchanged++
			}
		}
		ignored += len(p.Ignored)
		unmanaged += len(p.Unmanaged)
	}
	parts := []string{fmt.Sprintf("%d to change", changed), fmt.Sprintf("%d unchanged", unchanged)}
	if ignored > 0 {
		parts = append(parts, fmt.Sprintf("%d ignored", ignored))
	}
	if unmanaged > 0 {
		parts = append(parts, fmt.Sprintf("%d not in the config", unmanaged))
	}
	return "Plan: " + strings.Join(parts, ", ") + "."
}

// HasChanges reports whether any repository of the plans must change.
func HasChanges(plans []*OrgPlan) bool {
	for _, p := range plans {
		for _, r := range p.Repos {
			if r.HasChanges() {
				return true
			}
		}
	}
	return false
}
