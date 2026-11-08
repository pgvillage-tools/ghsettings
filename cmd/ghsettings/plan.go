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

func newPlanCmd() *cobra.Command {
	var orgs []string
	var repo string
	var detailed, verbose bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what would change, without changing anything",
		Long: `Compare the desired state from the config with the repositories on GitHub and show the differences.

The token for an org is read from GHSETTINGS_TOKEN_<ORG> (upper case, - becomes _), GITHUB_TOKEN or GH_TOKEN. It
needs read access to the administration of the repositories (repository settings and rulesets) and to their
contents (workflows).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			if len(orgs) == 0 {
				orgs = cfg.OrgNames()
			}
			var plans []*plan.OrgPlan
			for _, org := range orgs {
				if !slices.Contains(cfg.OrgNames(), org) {
					return fmt.Errorf("org %s is not in the config", org)
				}
				tok, tokErr := token(org)
				if tokErr != nil {
					return tokErr
				}
				planner := &plan.Planner{Config: cfg, API: github.NewClient(tok), Filter: repo}
				p, planErr := planner.PlanOrg(cmd.Context(), org)
				if planErr != nil {
					return planErr
				}
				if err = plan.Render(cmd.OutOrStdout(), p, verbose); err != nil {
					return err
				}
				plans = append(plans, p)
			}
			if _, err = fmt.Fprintln(cmd.OutOrStdout(), plan.Summary(plans)); err != nil {
				return err
			}
			if detailed && plan.HasChanges(plans) {
				return exitError(exitChanges)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&orgs, "org", "o", nil, "orgs to plan (default: all orgs in the config)")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "only repositories matching this glob")
	cmd.Flags().BoolVar(&detailed, "detailed-exitcode", false, "exit with 2 when there are changes")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also show unchanged repositories in full")
	return cmd
}
