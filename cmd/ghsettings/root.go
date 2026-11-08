// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ghsettings",
		Short:         "Manage the settings of GitHub repositories from a declarative config",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringP("config", "c", envOr("GHSETTINGS_CONFIG", "config"),
		"config file, or directory with *.yaml files (env GHSETTINGS_CONFIG)")
	root.AddCommand(newPlanCmd())
	return root
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// tokenEnv returns the environment variables that may hold a token for an org, most specific first.
func tokenEnv(org string) []string {
	suffix := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			return r
		default:
			return '_'
		}
	}, org)
	return []string{"GHSETTINGS_TOKEN_" + suffix, "GITHUB_TOKEN", "GH_TOKEN"}
}

// token returns the token for an org from the environment.
func token(org string) (string, error) {
	names := tokenEnv(org)
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("no token for org %s: set one of %s", org, strings.Join(names, ", "))
}
