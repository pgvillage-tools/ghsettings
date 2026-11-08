// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"github.com/pgvillage-tools/ghsettings/internal/github"
	"github.com/pgvillage-tools/ghsettings/internal/plan"
)

const exportHeader = `# Exported by ghsettings: the current state of the repositories of %s.
# Repositories with the same state share a profile.
`

func newExportCmd() *cobra.Command {
	var org, repo, ruleset, output string
	var settings []string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write the current state of the repositories of an org as config",
		Long: `Read the settings and the ruleset of the repositories of an org, and write them as config.
Repositories with the same state share a profile. Archived repositories are left out.

The export is checked by planning it: repositories whose plan is not empty (because the safety checks change their
state, or the config can't express it) are reported on stderr. The token is read as for plan.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tok, err := token(org)
			if err != nil {
				return err
			}
			api := github.NewClient(tok)
			exporter := &plan.Exporter{API: api, Filter: repo, Settings: settings, RulesetName: ruleset}
			cfg, warnings, err := exporter.Export(cmd.Context(), org)
			if err != nil {
				return err
			}
			data, err := yaml.MarshalWithOptions(cfg, yaml.Indent(2), yaml.IndentSequence(true))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if output != "" && output != "-" {
				f, createErr := os.Create(output)
				if createErr != nil {
					return createErr
				}
				defer func() { _ = f.Close() }()
				out = f
			}
			if _, err = fmt.Fprintf(out, exportHeader, org); err != nil {
				return err
			}
			if _, err = out.Write(data); err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			for _, w := range warnings {
				if _, err = fmt.Fprintf(stderr, "! %s\n", w); err != nil {
					return err
				}
			}
			// Check the export: planning it must not change anything
			p, err := (&plan.Planner{Config: cfg, API: api, Filter: repo}).PlanOrg(cmd.Context(), org)
			if err != nil {
				return err
			}
			return reportNotEmpty(stderr, p)
		},
	}
	cmd.Flags().StringVarP(&org, "org", "o", "", "org to export (required)")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "only repositories matching this glob")
	cmd.Flags().StringVar(&ruleset, "ruleset", "main", "name of the ruleset to export")
	cmd.Flags().StringSliceVar(&settings, "setting", plan.DefaultExportSettings, "repository settings to export")
	cmd.Flags().StringVarP(&output, "output", "f", "-", "file to write the config to (- for stdout)")
	_ = cmd.MarkFlagRequired("org")
	return cmd
}

// reportNotEmpty writes the plans of the repositories that would change, or a line that the plan is empty.
func reportNotEmpty(w io.Writer, p *plan.OrgPlan) error {
	notEmpty := &plan.OrgPlan{Org: p.Org}
	for _, r := range p.Repos {
		if r.HasChanges() {
			notEmpty.Repos = append(notEmpty.Repos, r)
		}
	}
	if len(notEmpty.Repos) == 0 {
		_, err := fmt.Fprintf(w, "Check: planning the export of %s changes nothing.\n", p.Org)
		return err
	}
	var buf bytes.Buffer
	if err := plan.Render(&buf, notEmpty, false); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "Check: planning the export changes %d repositories:\n%s", len(notEmpty.Repos),
		buf.String())
	return err
}
