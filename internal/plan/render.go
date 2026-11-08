// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan

import (
	"fmt"
	"io"
	"strings"
)

// printer writes formatted text and keeps the first error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

// Render writes a plan as text. Unchanged repositories are listed on one line, unless verbose is set.
func Render(out io.Writer, p *OrgPlan, verbose bool) error {
	w := &printer{w: out}
	var unchanged []string
	for _, r := range p.Repos {
		if !r.HasChanges() && len(r.Warnings) == 0 && !verbose {
			unchanged = append(unchanged, r.Repo)
			continue
		}
		renderRepo(w, r)
	}
	if len(unchanged) > 0 {
		w.printf("== %s: no changes: %s\n", p.Org, strings.Join(unchanged, ", "))
	}
	for _, list := range []struct {
		title string
		repos []string
	}{
		{"not in the config (no rule matches)", p.Unmanaged},
		{"ignored", p.Ignored},
		{"archived (skipped)", p.Archived},
	} {
		if len(list.repos) > 0 {
			w.printf("== %s: %s: %s\n", p.Org, list.title, strings.Join(list.repos, ", "))
		}
	}
	return w.err
}

func renderRepo(w *printer, r RepoPlan) {
	w.printf("== %s/%s (%s)\n", r.Org, r.Repo, r.Visibility)
	if len(r.Settings) > 0 {
		w.printf("  settings:\n")
		for _, c := range r.Settings {
			w.printf("    %s\n", c)
		}
	}
	if rs := r.Ruleset; rs != nil {
		queue := ""
		if rs.MergeQueue {
			queue = ", with merge queue"
		}
		switch {
		case rs.ID == 0:
			w.printf("  ruleset %q: create%s\n", rs.Name, queue)
		case len(rs.Changes) > 0:
			w.printf("  ruleset %q (%d): update%s\n", rs.Name, rs.ID, queue)
		default:
			w.printf("  ruleset %q (%d): no changes\n", rs.Name, rs.ID)
		}
		for _, c := range rs.Changes {
			w.printf("    %s\n", c)
		}
	}
	for _, warning := range r.Warnings {
		w.printf("  ! %s\n", warning)
	}
}
