// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgvillage-tools/ghsettings/internal/config"
)

const standard = `
standard:
  settings:
    allow_auto_merge: true
    allow_merge_commit: false
  ruleset:
    required_checks: [Validate PR Title]
    merge_queue:
      merge_method: SQUASH
    rules:
      deletion: {}
      pull_request:
        required_approving_review_count: 1
        allowed_merge_methods: [squash]
`

const orgs = `
profiles:
  go:
    ruleset:
      required_checks: [CI result, Validate PR Title]
orgs:
  example:
    settings:
      delete_branch_on_merge: true
    repos:
      - match: old-*
        ignore: true
      - match: special
        profile: go
        settings:
          allow_merge_commit: true
        ruleset:
          merge_queue: null
          rules:
            pull_request:
              required_approving_review_count: 2
      - match: "*"
        profile: go
`

func writeFiles(files map[string]string) string {
	dir := GinkgoT().TempDir()
	for name, content := range files {
		Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)).To(Succeed())
	}
	return dir
}

var _ = Describe("Load and Resolve", func() {
	var cfg *config.Config

	BeforeEach(func() {
		var err error
		cfg, err = config.Load(writeFiles(map[string]string{"standard.yaml": standard, "orgs.yml": orgs}))
		Expect(err).NotTo(HaveOccurred())
	})

	It("lists the orgs", func() {
		Expect(cfg.OrgNames()).To(Equal([]string{"example"}))
	})

	It("merges standard, org and profile", func() {
		d, res, err := cfg.Resolve("example", "tool")
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(config.Managed))
		Expect(res.String()).To(Equal("managed"))
		Expect(d.Settings).To(HaveKeyWithValue("allow_auto_merge", true))
		Expect(d.Settings).To(HaveKeyWithValue("allow_merge_commit", false))
		Expect(d.Settings).To(HaveKeyWithValue("delete_branch_on_merge", true))
		Expect(d.Ruleset.RequiredChecks).To(Equal([]string{"CI result", "Validate PR Title"}))
		Expect(d.Ruleset.MergeQueue).To(HaveKeyWithValue("merge_method", "SQUASH"))
		Expect(d.Ruleset.Rules).To(HaveKey("deletion"))
	})

	It("sets defaults", func() {
		d, _, err := cfg.Resolve("example", "tool")
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Ruleset.Name).To(Equal("main"))
		Expect(d.Ruleset.Enforcement).To(Equal("active"))
		Expect(d.Ruleset.Target).To(Equal("branch"))
		Expect(d.Ruleset.ChecksIntegrationID).To(BeEquivalentTo(config.ActionsAppID))
		Expect(d.Ruleset.Conditions).To(HaveKey("ref_name"))
	})

	It("applies repo overrides deeply, and null removes a key", func() {
		d, _, err := cfg.Resolve("example", "special")
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Settings).To(HaveKeyWithValue("allow_merge_commit", true))
		Expect(d.Ruleset.MergeQueue).To(BeNil())
		pr := d.Ruleset.Rules["pull_request"]
		Expect(pr).To(HaveKeyWithValue("required_approving_review_count", BeEquivalentTo(2)))
		Expect(pr).To(HaveKey("allowed_merge_methods"))
	})

	It("ignores repos of an ignore rule", func() {
		d, res, err := cfg.Resolve("example", "old-thing")
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(config.Ignored))
		Expect(d).To(BeNil())
	})

	It("reports unknown orgs as unmanaged", func() {
		_, res, err := cfg.Resolve("other", "tool")
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(Equal(config.Unmanaged))
	})
})

var _ = Describe("Load errors", func() {
	DescribeTable("rejects invalid configs",
		func(files map[string]string, msg string) {
			_, err := config.Load(writeFiles(files))
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("standard twice", map[string]string{"a.yaml": standard, "b.yaml": standard}, "standard already defined"),
		Entry("org twice", map[string]string{"a.yaml": orgs, "b.yaml": "orgs:\n  example:\n    repos: []\n"},
			`org "example" already defined`),
		Entry("profile twice", map[string]string{"a.yaml": orgs, "b.yaml": "profiles:\n  go: {}\n"},
			`profile "go" already defined`),
		Entry("unknown key", map[string]string{"a.yaml": "orgz: {}\n"}, "unknown field"),
		Entry("unknown profile", map[string]string{"a.yaml": "orgs:\n  x:\n    repos:\n      - match: '*'\n" +
			"        profile: nope\n"}, `unknown profile "nope"`),
		Entry("empty match", map[string]string{"a.yaml": "orgs:\n  x:\n    repos:\n      - profile: ''\n"},
			"match is empty"),
		Entry("bad glob", map[string]string{"a.yaml": "orgs:\n  x:\n    repos:\n      - match: '['\n"},
			"invalid match"),
		Entry("no files", map[string]string{}, "no *.yaml"),
	)

	It("fails on a missing path", func() {
		_, err := config.Load(filepath.Join(GinkgoT().TempDir(), "missing"))
		Expect(err).To(HaveOccurred())
	})

	It("reads a single file", func() {
		dir := writeFiles(map[string]string{"one.yaml": orgs})
		cfg, err := config.Load(filepath.Join(dir, "one.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.OrgNames()).To(Equal([]string{"example"}))
	})

	DescribeTable("rejects invalid rulesets on resolve",
		func(ruleset, msg string) {
			cfg, err := config.Load(writeFiles(map[string]string{
				"a.yaml": "orgs:\n  x:\n    repos:\n      - match: '*'\n        ruleset:\n" + ruleset,
			}))
			Expect(err).NotTo(HaveOccurred())
			_, _, err = cfg.Resolve("x", "repo")
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("unknown field", "          bogus: 1\n", "unknown field"),
		Entry("required_status_checks rule", "          rules:\n            required_status_checks: {}\n",
			"use required_checks"),
		Entry("merge_queue rule", "          rules:\n            merge_queue: {}\n", "use merge_queue"),
	)
})
