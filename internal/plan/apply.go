// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan

import (
	"context"
	"fmt"
	"io"
)

// WriteAPI is the part of the GitHub API that applying needs.
type WriteAPI interface {
	UpdateRepo(ctx context.Context, owner, repo string, fields map[string]any) error
	CreateRuleset(ctx context.Context, owner, repo string, ruleset any) (int64, error)
	UpdateRuleset(ctx context.Context, owner, repo string, id int64, ruleset any) error
}

// Apply makes the changes of a plan. Only repositories with changes are touched: settings with one PATCH of the
// changed fields, the ruleset with a POST (new) or a PUT (existing). A PUT keeps what the config doesn't manage:
// bypass actors (left out of the request) and rule parameters that the config doesn't set (taken from the current
// ruleset). It writes one line per change to w, and stops at the first error.
func Apply(ctx context.Context, api WriteAPI, p *OrgPlan, w io.Writer) error {
	out := &printer{w: w}
	for _, r := range p.Repos {
		if !r.HasChanges() {
			continue
		}
		if len(r.Settings) > 0 {
			fields := map[string]any{}
			for _, c := range r.Settings {
				fields[c.Path] = c.Desired
			}
			if err := api.UpdateRepo(ctx, r.Org, r.Repo, fields); err != nil {
				return fmt.Errorf("%s/%s: updating settings: %w", r.Org, r.Repo, err)
			}
			out.printf("%s/%s: settings updated (%d)\n", r.Org, r.Repo, len(fields))
		}
		if err := applyRuleset(ctx, api, r, out); err != nil {
			return err
		}
	}
	return out.err
}

func applyRuleset(ctx context.Context, api WriteAPI, r RepoPlan, out *printer) error {
	rs := r.Ruleset
	switch {
	case rs == nil:
		return nil
	case rs.ID == 0:
		id, err := api.CreateRuleset(ctx, r.Org, r.Repo, rs.Body)
		if err != nil {
			return fmt.Errorf("%s/%s: creating ruleset %q: %w", r.Org, r.Repo, rs.Name, err)
		}
		out.printf("%s/%s: ruleset %q created (%d)\n", r.Org, r.Repo, rs.Name, id)
	case len(rs.Changes) > 0:
		if err := api.UpdateRuleset(ctx, r.Org, r.Repo, rs.ID, mergeRuleset(rs.Body, rs.Current)); err != nil {
			return fmt.Errorf("%s/%s: updating ruleset %q: %w", r.Org, r.Repo, rs.Name, err)
		}
		out.printf("%s/%s: ruleset %q updated (%d)\n", r.Org, r.Repo, rs.Name, rs.ID)
	}
	return nil
}

// mergeRuleset returns the desired ruleset, with the parameters of the current rules that the desired rules don't
// set. Rules that are not desired are left out, so they are removed.
func mergeRuleset(desired, current *Ruleset) *Ruleset {
	currentParams := map[string]map[string]any{}
	if current != nil {
		for _, rule := range current.Rules {
			currentParams[rule.Type] = rule.Parameters
		}
	}
	merged := &Ruleset{Name: desired.Name, Target: desired.Target, Enforcement: desired.Enforcement,
		Conditions: desired.Conditions}
	for _, rule := range desired.Rules {
		params := map[string]any{}
		for k, v := range currentParams[rule.Type] {
			params[k] = v
		}
		for k, v := range rule.Parameters {
			params[k] = v
		}
		if len(params) == 0 {
			params = nil
		}
		merged.Rules = append(merged.Rules, Rule{Type: rule.Type, Parameters: params})
	}
	return merged
}
