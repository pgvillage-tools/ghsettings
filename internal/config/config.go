// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

// Package config reads the desired settings of repositories from YAML files and resolves them per repository.
//
// The desired state of a repository is built from layers, each overriding the previous one:
//
//	standard -> org -> profile -> repo rule
//
// A layer has free-form `settings` (fields of the GitHub repository API) and a `ruleset`. Maps are merged
// deeply, lists and scalars are replaced, and a null value removes the key (e.g. `merge_queue: null`).
package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/goccy/go-yaml"
)

// Layer is one level of desired settings.
type Layer struct {
	// Settings are fields of the GitHub repository API (PATCH /repos/{owner}/{repo}), e.g. allow_auto_merge.
	Settings map[string]any `yaml:"settings,omitempty"`
	// Ruleset is the desired branch ruleset; see Ruleset for the fields.
	Ruleset map[string]any `yaml:"ruleset,omitempty"`
}

// RepoRule assigns a profile and overrides to the repositories matching a glob. The first matching rule of an org
// applies.
type RepoRule struct {
	// Match is a glob (path.Match syntax) on the repository name.
	Match string `yaml:"match"`
	// Profile is the name of a profile; empty for none.
	Profile string `yaml:"profile,omitempty"`
	// Ignore leaves the matching repositories alone.
	Ignore bool  `yaml:"ignore,omitempty"`
	Layer  Layer `yaml:",inline"`
}

// Org is the config of one GitHub organization.
type Org struct {
	Layer Layer      `yaml:",inline"`
	Repos []RepoRule `yaml:"repos"`
}

// Config is the merged config of all files.
type Config struct {
	Standard *Layer            `yaml:"standard,omitempty"`
	Profiles map[string]Layer  `yaml:"profiles,omitempty"`
	Orgs     map[string]Org    `yaml:"orgs,omitempty"`
	files    map[string]string // where each profile and org was defined, for error messages
}

// Load reads a YAML file, or all *.yaml and *.yml files in a directory, and merges them. `standard` may be set in
// one file only, and every profile and org may be defined only once.
func Load(p string) (*Config, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	files := []string{p}
	if info.IsDir() {
		files = nil
		for _, pattern := range []string{"*.yaml", "*.yml"} {
			matches, globErr := filepath.Glob(filepath.Join(p, pattern))
			if globErr != nil {
				return nil, globErr
			}
			files = append(files, matches...)
		}
		sort.Strings(files)
		if len(files) == 0 {
			return nil, fmt.Errorf("no *.yaml or *.yml files in %s", p)
		}
	}
	cfg := &Config{Profiles: map[string]Layer{}, Orgs: map[string]Org{}, files: map[string]string{}}
	standardFile := ""
	for _, f := range files {
		part, parseErr := parseFile(f)
		if parseErr != nil {
			return nil, parseErr
		}
		if part.Standard != nil {
			if standardFile != "" {
				return nil, fmt.Errorf("%s: standard already defined in %s", f, standardFile)
			}
			cfg.Standard, standardFile = part.Standard, f
		}
		for name, profile := range part.Profiles {
			if prev, ok := cfg.files["profile/"+name]; ok {
				return nil, fmt.Errorf("%s: profile %q already defined in %s", f, name, prev)
			}
			cfg.Profiles[name], cfg.files["profile/"+name] = profile, f
		}
		for name, org := range part.Orgs {
			if prev, ok := cfg.files["org/"+name]; ok {
				return nil, fmt.Errorf("%s: org %q already defined in %s", f, name, prev)
			}
			cfg.Orgs[name], cfg.files["org/"+name] = org, f
		}
	}
	return cfg, cfg.validate()
}

func parseFile(f string) (*Config, error) {
	data, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	var part Config
	if err = yaml.UnmarshalWithOptions(data, &part, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", f, err)
	}
	return &part, nil
}

func (c *Config) validate() error {
	var errs []error
	for _, name := range c.OrgNames() {
		for i, rule := range c.Orgs[name].Repos {
			where := fmt.Sprintf("%s: org %q, repos[%d]", c.files["org/"+name], name, i)
			if rule.Match == "" {
				errs = append(errs, fmt.Errorf("%s: match is empty", where))
			} else if _, err := path.Match(rule.Match, ""); err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid match %q: %w", where, rule.Match, err))
			}
			if rule.Profile != "" {
				if _, ok := c.Profiles[rule.Profile]; !ok {
					errs = append(errs, fmt.Errorf("%s: unknown profile %q", where, rule.Profile))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// OrgNames returns the names of the configured orgs, sorted.
func (c *Config) OrgNames() []string {
	names := make([]string, 0, len(c.Orgs))
	for name := range c.Orgs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolution is the outcome of resolving a repository against the config.
type Resolution int

const (
	// Unmanaged means that no rule matches the repository.
	Unmanaged Resolution = iota
	// Ignored means that the first matching rule ignores the repository.
	Ignored
	// Managed means that the repository has a desired state.
	Managed
)

// Resolve returns the desired state of a repository of an org, by merging the standard, the org, the profile and
// the repo rule of the first rule that matches.
func (c *Config) Resolve(org, repo string) (*Desired, Resolution, error) {
	orgCfg, ok := c.Orgs[org]
	if !ok {
		return nil, Unmanaged, nil
	}
	for _, rule := range orgCfg.Repos {
		if matched, _ := path.Match(rule.Match, repo); !matched {
			continue
		}
		if rule.Ignore {
			return nil, Ignored, nil
		}
		layers := []Layer{}
		if c.Standard != nil {
			layers = append(layers, *c.Standard)
		}
		layers = append(layers, orgCfg.Layer)
		if rule.Profile != "" {
			layers = append(layers, c.Profiles[rule.Profile])
		}
		layers = append(layers, rule.Layer)
		desired, err := newDesired(mergeLayers(layers...))
		if err != nil {
			return nil, Managed, fmt.Errorf("%s/%s: %w", org, repo, err)
		}
		return desired, Managed, nil
	}
	return nil, Unmanaged, nil
}

func mergeLayers(layers ...Layer) Layer {
	merged := Layer{Settings: map[string]any{}, Ruleset: map[string]any{}}
	for _, l := range layers {
		merged.Settings = mergeMaps(merged.Settings, l.Settings)
		merged.Ruleset = mergeMaps(merged.Ruleset, l.Ruleset)
	}
	return merged
}

// mergeMaps merges src into a copy of dst: maps are merged deeply, other values replaced, and nil removes the key.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if v == nil {
			delete(out, k)
			continue
		}
		srcMap, srcIsMap := v.(map[string]any)
		dstMap, dstIsMap := out[k].(map[string]any)
		if srcIsMap && dstIsMap {
			out[k] = mergeMaps(dstMap, srcMap)
			continue
		}
		if srcIsMap {
			out[k] = mergeMaps(nil, srcMap)
			continue
		}
		out[k] = v
	}
	return out
}

// String returns the name of a resolution.
func (r Resolution) String() string {
	return [...]string{"unmanaged", "ignored", "managed"}[r]
}
