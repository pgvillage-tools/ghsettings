// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/pgvillage-tools/ghsettings/internal/config"
)

// DefaultExportSettings are the repository settings that Export reads by default.
var DefaultExportSettings = []string{
	"allow_auto_merge",
	"allow_merge_commit",
	"allow_rebase_merge",
	"allow_squash_merge",
	"allow_update_branch",
	"delete_branch_on_merge",
}

const minPrefixLen = 3

// Exporter reads the current state of repositories and writes it as config.
type Exporter struct {
	API API
	// Filter limits the export to the repositories matching this glob; empty for all.
	Filter string
	// Settings are the repository settings to export (default: DefaultExportSettings).
	Settings []string
	// RulesetName is the name of the ruleset to export (default: main).
	RulesetName string
}

// Export returns the current state of the repositories of an org as config. Repositories with the same state share a
// profile. Archived repositories are left out. The warnings name state that the config can't express exactly.
func (e *Exporter) Export(ctx context.Context, org string) (*config.Config, []string, error) {
	keys := e.Settings
	if len(keys) == 0 {
		keys = DefaultExportSettings
	}
	name := e.RulesetName
	if name == "" {
		name = "main"
	}
	repos, err := e.API.ListRepos(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	var names []string
	layers := map[string]config.Layer{}
	var warnings []string
	for _, r := range repos {
		if r.Archived {
			continue
		}
		if matched, _ := path.Match(e.Filter, r.Name); e.Filter != "" && !matched {
			continue
		}
		layer, repoWarnings, layerErr := e.exportRepo(ctx, org, r.Name, keys, name)
		if layerErr != nil {
			return nil, nil, layerErr
		}
		warnings = append(warnings, repoWarnings...)
		names = append(names, r.Name)
		layers[r.Name] = layer
	}
	cfg := &config.Config{Profiles: map[string]config.Layer{}, Orgs: map[string]config.Org{}}
	cfg.Orgs[org] = config.Org{Repos: group(names, layers, cfg.Profiles)}
	return cfg, warnings, nil
}

func (e *Exporter) exportRepo(ctx context.Context, org, repo string, keys []string,
	name string) (config.Layer, []string, error) {
	layer := config.Layer{}
	current, err := e.API.GetRepo(ctx, org, repo)
	if err != nil {
		return layer, nil, err
	}
	var warnings []string
	layer.Settings = map[string]any{}
	for _, k := range keys {
		v, ok := current[k]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s/%s: setting %q is not in the API response", org, repo, k))
			continue
		}
		layer.Settings[k] = v
	}
	raw, err := e.API.FindRuleset(ctx, org, repo, name)
	if err != nil || raw == nil {
		return layer, warnings, err
	}
	rs, err := decodeRuleset(raw)
	if err != nil {
		return layer, nil, fmt.Errorf("%s/%s: ruleset %q: %w", org, repo, name, err)
	}
	ruleset, rsWarnings := exportRuleset(rs)
	for _, w := range rsWarnings {
		warnings = append(warnings, fmt.Sprintf("%s/%s: %s", org, repo, w))
	}
	layer.Ruleset = ruleset
	return layer, warnings, nil
}

// exportRuleset turns a ruleset from the API into the ruleset of a config layer, leaving out defaults.
func exportRuleset(rs *Ruleset) (map[string]any, []string) {
	out := map[string]any{}
	defaults := &config.Ruleset{}
	defaults.SetDefaults()
	if rs.Name != defaults.Name {
		out["name"] = rs.Name
	}
	if rs.Enforcement != defaults.Enforcement {
		out["enforcement"] = rs.Enforcement
	}
	if rs.Target != defaults.Target {
		out["target"] = rs.Target
	}
	if !reflect.DeepEqual(canonical(rs.Conditions), canonical(defaults.Conditions)) {
		out["conditions"] = canonicalMap(rs.Conditions)
	}
	var warnings []string
	rules := map[string]any{}
	for _, rule := range rs.Rules {
		params := cleanParams(canonicalMap(rule.Parameters))
		switch rule.Type {
		case "merge_queue":
			out["merge_queue"] = params
		case "required_status_checks":
			checks, w := exportChecks(rule.Parameters, out)
			warnings = append(warnings, w...)
			out["required_checks"] = checks
		default:
			rules[rule.Type] = params
		}
	}
	if len(rules) > 0 {
		out["rules"] = rules
	}
	if checks, ok := out["required_checks"].([]any); ok {
		strict := lookupBool(rs, "required_status_checks", "strict_required_status_checks_policy")
		_, queue := out["merge_queue"]
		if len(checks) > 0 && strict == queue {
			warnings = append(warnings, fmt.Sprintf("strict required checks is %v with merge queue %v; ghsettings "+
				"makes checks strict exactly when there is no merge queue", strict, queue))
		}
	}
	return out, warnings
}

// exportChecks returns the names of the required checks, and sets checks_integration_id when they all come from one
// app other than GitHub Actions.
func exportChecks(params map[string]any, out map[string]any) ([]any, []string) {
	list, ok := canonicalMap(params)["required_status_checks"].([]any)
	if !ok {
		return []any{}, nil
	}
	var names []string
	ids := map[string]bool{}
	for _, e := range list {
		c, isMap := e.(map[string]any)
		if !isMap {
			continue
		}
		names = append(names, fmt.Sprint(c["context"]))
		ids[fmt.Sprint(c["integration_id"])] = true
	}
	sort.Strings(names)
	checks := make([]any, len(names))
	for i, n := range names {
		checks[i] = n
	}
	var warnings []string
	switch {
	case len(ids) > 1:
		warnings = append(warnings, "required checks come from several apps; ghsettings binds them all to one app")
	case len(ids) == 1:
		for id := range ids {
			if id != fmt.Sprint(config.ActionsAppID) {
				if n, err := strconv.Atoi(id); err == nil {
					out["checks_integration_id"] = n
				} else {
					warnings = append(warnings, fmt.Sprintf("required checks are not bound to an app (%s)", id))
				}
			}
		}
	}
	return checks, warnings
}

func lookupBool(rs *Ruleset, ruleType, param string) bool {
	for _, r := range rs.Rules {
		if b, ok := r.Parameters[param].(bool); ok && r.Type == ruleType {
			return b
		}
	}
	return false
}

// cleanParams leaves out parameters without content (empty lists and maps, and maps with only empty values like
// {"enabled": false, "allowed_actors": []}), which the API returns but the config doesn't need.
func cleanParams(params map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range params {
		if !isEmpty(v) {
			out[k] = v
		}
	}
	return out
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		for _, child := range t {
			if b, ok := child.(bool); ok && !b {
				continue
			}
			if !isEmpty(child) {
				return false
			}
		}
		return true
	}
	return false
}

// group gives repositories with the same state a shared profile, and returns the repo rules.
func group(names []string, layers map[string]config.Layer, profiles map[string]config.Layer) []config.RepoRule {
	byKey := map[string][]string{}
	var keys []string
	for _, n := range names {
		data, _ := json.Marshal(layers[n])
		k := string(data)
		if _, ok := byKey[k]; !ok {
			keys = append(keys, k)
		}
		byKey[k] = append(byKey[k], n)
	}
	var rules []config.RepoRule
	for i, k := range keys {
		members := byKey[k]
		if len(members) == 1 {
			rules = append(rules, config.RepoRule{Match: members[0], Layer: layers[members[0]]})
			continue
		}
		prefix := commonPrefix(members)
		profile := strings.TrimRight(prefix, "-_")
		if len(profile) < minPrefixLen {
			profile = fmt.Sprintf("group-%d", i+1)
		}
		if _, taken := profiles[profile]; taken {
			profile = fmt.Sprintf("%s-%d", profile, i+1)
		}
		profiles[profile] = layers[members[0]]
		if len(prefix) >= minPrefixLen && matchesExactly(prefix+"*", members, names) {
			rules = append(rules, config.RepoRule{Match: prefix + "*", Profile: profile})
			continue
		}
		for _, m := range members {
			rules = append(rules, config.RepoRule{Match: m, Profile: profile})
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Match < rules[j].Match })
	return rules
}

// commonPrefix returns the longest common prefix of names, cut after the last - or _.
func commonPrefix(names []string) string {
	prefix := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	if i := strings.LastIndexAny(prefix, "-_"); i >= 0 {
		return prefix[:i+1]
	}
	return ""
}

// matchesExactly reports whether glob matches exactly the members among all names.
func matchesExactly(glob string, members, all []string) bool {
	count := 0
	for _, n := range all {
		if matched, _ := path.Match(glob, n); matched {
			count++
		}
	}
	return count == len(members)
}
