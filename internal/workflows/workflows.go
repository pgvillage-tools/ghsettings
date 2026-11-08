// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

// Package workflows finds out which GitHub Actions workflow provides a status check, and whether that workflow runs
// in the merge queue (has a merge_group trigger).
package workflows

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Workflow is the part of a workflow file that matters for required checks.
type Workflow struct {
	Path       string
	MergeGroup bool
	Jobs       []Job
}

// Job is a job of a workflow.
type Job struct {
	ID   string
	Name string
	// Matrix is true for a job with a strategy.matrix: its checks get the matrix values appended, e.g. "build
	// (amd64)".
	Matrix bool
	// Reusable is true for a job that calls a reusable workflow: its checks are named "<job> / <called job>".
	Reusable bool
}

type rawWorkflow struct {
	On   any               `yaml:"on"`
	Jobs map[string]rawJob `yaml:"jobs"`
}

type rawJob struct {
	Name     string         `yaml:"name"`
	Uses     string         `yaml:"uses"`
	Strategy map[string]any `yaml:"strategy"`
}

// Parse reads a workflow file.
func Parse(path string, data []byte) (*Workflow, error) {
	var raw rawWorkflow
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	w := &Workflow{Path: path, MergeGroup: hasTrigger(raw.On, "merge_group")}
	ids := make([]string, 0, len(raw.Jobs))
	for id := range raw.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		j := raw.Jobs[id]
		_, matrix := j.Strategy["matrix"]
		w.Jobs = append(w.Jobs, Job{ID: id, Name: j.Name, Matrix: matrix, Reusable: j.Uses != ""})
	}
	return w, nil
}

// hasTrigger handles the three forms of `on`: a string, a list, and a map.
func hasTrigger(on any, event string) bool {
	switch v := on.(type) {
	case string:
		return v == event
	case []any:
		for _, e := range v {
			if e == event {
				return true
			}
		}
	case map[string]any:
		_, ok := v[event]
		return ok
	}
	return false
}

var expression = regexp.MustCompile(`\$\{\{.*?\}\}`)

// Provides reports whether the job reports a check with this name.
func (j Job) Provides(check string) bool {
	name := j.Name
	if name == "" {
		name = j.ID
	}
	// ${{ ... }} in the name becomes a wildcard
	parts := expression.Split(name, -1)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	pattern := strings.Join(parts, ".+")
	alternatives := []string{pattern}
	if j.Matrix {
		alternatives = append(alternatives, pattern+` \(.+\)`)
	}
	if j.Reusable {
		alternatives = append(alternatives, pattern+` / .+`)
	}
	re := regexp.MustCompile(`^(` + strings.Join(alternatives, "|") + `)$`)
	return re.MatchString(check)
}

// Set is the workflows of a repository.
type Set []*Workflow

// ParseAll parses the workflow files of a repository, by path.
func ParseAll(files map[string][]byte) (Set, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	set := Set{}
	for _, p := range paths {
		w, err := Parse(p, files[p])
		if err != nil {
			return nil, err
		}
		set = append(set, w)
	}
	return set, nil
}

// Find returns the first workflow with a job that provides the check, or nil.
func (s Set) Find(check string) *Workflow {
	for _, w := range s {
		for _, j := range w.Jobs {
			if j.Provides(check) {
				return w
			}
		}
	}
	return nil
}
