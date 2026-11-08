// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Change is one difference between the current and the desired state. A missing side means that the field or rule
// is absent there.
type Change struct {
	Path       string
	Current    any
	HasCurrent bool
	Desired    any
	HasDesired bool
}

// String renders a change as "~ path: current -> desired", "+ path: desired" or "- path: current".
func (c Change) String() string {
	switch {
	case !c.HasCurrent:
		return fmt.Sprintf("+ %s: %s", c.Path, render(c.Desired))
	case !c.HasDesired:
		return fmt.Sprintf("- %s: %s", c.Path, render(c.Current))
	default:
		return fmt.Sprintf("~ %s: %s -> %s", c.Path, render(c.Current), render(c.Desired))
	}
}

func render(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// canonical turns a value into its JSON form (maps, slices, float64, string, bool, nil), so values decoded from YAML
// and from the API compare equal.
func canonical(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err = json.Unmarshal(data, &out); err != nil {
		return v
	}
	return out
}

// diffMaps compares two flat maps and returns the changes, sorted by path.
func diffMaps(current, desired map[string]any) []Change {
	paths := map[string]bool{}
	for k := range current {
		paths[k] = true
	}
	for k := range desired {
		paths[k] = true
	}
	sorted := make([]string, 0, len(paths))
	for k := range paths {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var changes []Change
	for _, p := range sorted {
		cur, hasCur := current[p]
		des, hasDes := desired[p]
		if hasCur && hasDes && reflect.DeepEqual(canonical(cur), canonical(des)) {
			continue
		}
		changes = append(changes, Change{Path: p, Current: cur, HasCurrent: hasCur, Desired: des, HasDesired: hasDes})
	}
	return changes
}

// flatten turns nested maps into "a.b.c" paths. Lists are kept as values; lists of scalars are sorted, so the order
// doesn't count as a change.
func flatten(prefix string, v any, out map[string]any) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		out[prefix] = sortScalars(v)
		return
	}
	for k, child := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		flatten(p, child, out)
	}
}

func sortScalars(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	strs := make([]string, 0, len(list))
	for _, e := range list {
		s, isStr := e.(string)
		if !isStr {
			return v
		}
		strs = append(strs, s)
	}
	sort.Strings(strs)
	out := make([]any, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out
}

// Ruleset is a ruleset as the API sends and returns it (the fields that ghsettings manages).
type Ruleset struct {
	ID          int64          `json:"id,omitempty"`
	Name        string         `json:"name"`
	Target      string         `json:"target"`
	Enforcement string         `json:"enforcement"`
	Conditions  map[string]any `json:"conditions"`
	Rules       []Rule         `json:"rules"`
}

// Rule is a rule of a ruleset.
type Rule struct {
	Type       string         `json:"type"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// decodeRuleset turns a ruleset from the API into a Ruleset.
func decodeRuleset(raw map[string]any) (*Ruleset, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	rs := &Ruleset{}
	return rs, json.Unmarshal(data, rs)
}

// canonicalMap is canonical for a map.
func canonicalMap(m map[string]any) map[string]any {
	if out, ok := canonical(m).(map[string]any); ok {
		return out
	}
	return m
}

// rulesetView turns a ruleset into a flat map for comparing. Rule parameters are limited to the keys in managed (by
// rule type), when managed has the type: fields that the config doesn't set are not compared. A rule without
// (managed) parameters is shown as true.
func rulesetView(rs *Ruleset, managed map[string]map[string]bool) map[string]any {
	view := map[string]any{"name": rs.Name, "target": rs.Target, "enforcement": rs.Enforcement}
	if rs.Conditions != nil {
		flatten("conditions", canonicalMap(rs.Conditions), view)
	}
	for _, rule := range rs.Rules {
		keep := map[string]any{}
		for k, v := range canonicalMap(rule.Parameters) {
			if keys, ok := managed[rule.Type]; ok && !keys[k] {
				continue
			}
			if k == "required_status_checks" {
				v = checkNames(v)
			}
			keep[k] = v
		}
		if len(keep) == 0 {
			view["rules."+rule.Type] = true
			continue
		}
		flatten("rules."+rule.Type, keep, view)
	}
	return view
}

// checkNames renders required status checks as sorted "context (app id)" strings.
func checkNames(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	names := make([]any, 0, len(list))
	for _, e := range list {
		c, isMap := e.(map[string]any)
		if !isMap {
			return v
		}
		name := fmt.Sprint(c["context"])
		if id, hasID := c["integration_id"]; hasID && id != nil {
			name += fmt.Sprintf(" (%v)", id)
		}
		names = append(names, name)
	}
	return sortScalars(names)
}

// managedParams returns the parameter keys per rule type of a ruleset.
func managedParams(rs *Ruleset) map[string]map[string]bool {
	managed := map[string]map[string]bool{}
	for _, rule := range rs.Rules {
		keys := map[string]bool{}
		for k := range rule.Parameters {
			keys[k] = true
		}
		managed[rule.Type] = keys
	}
	return managed
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}
