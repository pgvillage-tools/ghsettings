// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/pgvillage-tools/ghsettings/internal/config"
	"github.com/pgvillage-tools/ghsettings/internal/github"
	"github.com/pgvillage-tools/ghsettings/internal/plan"
)

// exitChanges is the exit code of plan --detailed-exitcode when there are changes
const exitChanges = 2

const tokenHelp = `The token for an org is read from GHSETTINGS_TOKEN_<ORG> (upper case, - becomes _),
GITHUB_TOKEN or GH_TOKEN.`

// planFlags are the flags that plan and apply share.
type planFlags struct {
	orgs    []string
	repo    string
	verbose bool
}

func (f *planFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringSliceVarP(&f.orgs, "org", "o", nil, "orgs (default: all orgs in the config)")
	cmd.Flags().StringVarP(&f.repo, "repo", "r", "", "only repositories matching this glob")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "also show unchanged repositories in full")
}

// eachPlan loads the config, and plans and renders every org; then it calls fn with the plan and the client of the
// org. It returns all plans.
func (f *planFlags) eachPlan(cmd *cobra.Command, fn func(*github.Client, *plan.OrgPlan) error) ([]*plan.OrgPlan,
	error) {
	cfgPath, _ := cmd.Flags().GetString("config")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	orgs := f.orgs
	if len(orgs) == 0 {
		orgs = cfg.OrgNames()
	}
	var plans []*plan.OrgPlan
	for _, org := range orgs {
		if !slices.Contains(cfg.OrgNames(), org) {
			return nil, fmt.Errorf("org %s is not in the config", org)
		}
		tok, tokErr := token(org)
		if tokErr != nil {
			return nil, tokErr
		}
		client := github.NewClient(tok)
		p, planErr := (&plan.Planner{Config: cfg, API: client, Filter: f.repo}).PlanOrg(cmd.Context(), org)
		if planErr != nil {
			return nil, planErr
		}
		if err = plan.Render(cmd.OutOrStdout(), p, f.verbose); err != nil {
			return nil, err
		}
		plans = append(plans, p)
		if fn != nil {
			if err = fn(client, p); err != nil {
				return nil, err
			}
		}
	}
	if _, err = fmt.Fprintln(cmd.OutOrStdout(), plan.Summary(plans)); err != nil {
		return nil, err
	}
	return plans, nil
}

func newPlanCmd() *cobra.Command {
	var flags planFlags
	var detailed bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what would change, without changing anything",
		Long: `Compare the desired state from the config with the repositories on GitHub and show the differences.

` + tokenHelp + ` It needs read access to the administration of the repositories (settings and
rulesets) and to their contents (workflows).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			plans, err := flags.eachPlan(cmd, nil)
			if err != nil {
				return err
			}
			if detailed && plan.HasChanges(plans) {
				return exitError(exitChanges)
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVar(&detailed, "detailed-exitcode", false, "exit with 2 when there are changes")
	return cmd
}

func newApplyCmd() *cobra.Command {
	var flags planFlags
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Change the repositories to the desired state",
		Long: `Plan, show the plan, and make the changes: repository settings with one PATCH of the changed fields, and
the ruleset with a POST (new) or PUT (existing). A PUT keeps bypass actors and the rule parameters that the config
doesn't set. Repositories without changes are not touched.

` + tokenHelp + ` It needs write access to the administration of the repositories, and read access to
their contents (workflows).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := flags.eachPlan(cmd, func(client *github.Client, p *plan.OrgPlan) error {
				return plan.Apply(cmd.Context(), client, p, cmd.OutOrStdout())
			})
			return err
		},
	}
	flags.register(cmd)
	return cmd
}
