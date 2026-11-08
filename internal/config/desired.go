// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ActionsAppID is the id of the GitHub Actions app. Required checks are bound to it by default, so a check with the
// same name from another app can't satisfy the rule.
const ActionsAppID = 15368

// Desired is the resolved desired state of one repository.
type Desired struct {
	// Settings are fields of the repository API, compared with GET /repos/{owner}/{repo}.
	Settings map[string]any
	// Ruleset is nil when no ruleset is managed.
	Ruleset *Ruleset
}

// Ruleset is the desired branch ruleset of a repository.
type Ruleset struct {
	// Name identifies the ruleset of the repository (default: main).
	Name string `json:"name"`
	// Enforcement is active, evaluate or disabled (default: active).
	Enforcement string `json:"enforcement"`
	// Target is the ruleset target (default: branch).
	Target string `json:"target"`
	// Conditions default to the default branch.
	Conditions map[string]any `json:"conditions"`
	// Rules maps a rule type to its parameters, e.g. {"deletion": {}, "pull_request": {...}}. The parameters are
	// sent to GitHub as they are, so every rule and parameter of the API can be used.
	Rules map[string]map[string]any `json:"rules"`
	// RequiredChecks are the names of the required status checks. A check is only required when a workflow of
	// the repository has a job that provides it.
	RequiredChecks []string `json:"required_checks"`
	// ChecksIntegrationID is the app the required checks must come from (default: GitHub Actions).
	ChecksIntegrationID int64 `json:"checks_integration_id"`
	// MergeQueue holds the parameters of the merge_queue rule. The queue is only enabled where it works: on public
	// repositories where every workflow that provides a required check has a merge_group trigger. Elsewhere the
	// required checks are strict (branches must be up to date) instead.
	MergeQueue map[string]any `json:"merge_queue"`
}

func newDesired(l Layer) (*Desired, error) {
	d := &Desired{Settings: l.Settings}
	if len(l.Ruleset) == 0 {
		return d, nil
	}
	data, err := json.Marshal(l.Ruleset)
	if err != nil {
		return nil, fmt.Errorf("ruleset: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	rs := &Ruleset{}
	if err = dec.Decode(rs); err != nil {
		return nil, fmt.Errorf("ruleset: %w", err)
	}
	rs.SetDefaults()
	if err = rs.validate(); err != nil {
		return nil, fmt.Errorf("ruleset: %w", err)
	}
	d.Ruleset = rs
	return d, nil
}

// SetDefaults fills in the defaults of fields that are not set.
func (r *Ruleset) SetDefaults() {
	if r.Name == "" {
		r.Name = "main"
	}
	if r.Enforcement == "" {
		r.Enforcement = "active"
	}
	if r.Target == "" {
		r.Target = "branch"
	}
	if r.Conditions == nil {
		r.Conditions = map[string]any{
			"ref_name": map[string]any{"include": []any{"~DEFAULT_BRANCH"}, "exclude": []any{}},
		}
	}
	if r.ChecksIntegrationID == 0 {
		r.ChecksIntegrationID = ActionsAppID
	}
	if r.Rules == nil {
		r.Rules = map[string]map[string]any{}
	}
}

func (r *Ruleset) validate() error {
	var errs []error
	for _, managed := range []string{"required_status_checks", "merge_queue"} {
		if _, ok := r.Rules[managed]; ok {
			errs = append(errs, fmt.Errorf("rules.%s: use %s instead", managed,
				map[string]string{"required_status_checks": "required_checks", "merge_queue": "merge_queue"}[managed]))
		}
	}
	return errors.Join(errs...)
}
