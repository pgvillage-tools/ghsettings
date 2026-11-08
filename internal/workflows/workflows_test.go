// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package workflows_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgvillage-tools/ghsettings/internal/workflows"
)

const ci = `
name: CI
on:
  pull_request:
  merge_group:
jobs:
  lint:
    runs-on: ubuntu-latest
  test:
    name: Run unit tests
    runs-on: ubuntu-latest
  build:
    strategy:
      matrix:
        arch: [amd64, arm64]
  e2e:
    name: e2e (${{ matrix.pg }})
    strategy:
      matrix:
        pg: [14, 18]
  release:
    uses: ./.github/workflows/release.yml
`

const title = `
on:
  pull_request_target:
    types: [opened]
jobs:
  validate:
    name: Validate PR Title
`

var _ = Describe("Parse", func() {
	DescribeTable("detects the merge_group trigger",
		func(on string, want bool) {
			w, err := workflows.Parse("w.yml", []byte("on: "+on+"\njobs: {}\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(w.MergeGroup).To(Equal(want))
		},
		Entry("string", "merge_group", true),
		Entry("other string", "push", false),
		Entry("list", "[pull_request, merge_group]", true),
		Entry("list without", "[pull_request]", false),
		Entry("map", "{merge_group: {}, push: {}}", true),
		Entry("map without", "{push: {}}", false),
	)

	It("fails on invalid YAML", func() {
		_, err := workflows.Parse("w.yml", []byte("jobs: [\n"))
		Expect(err).To(MatchError(ContainSubstring("w.yml")))
	})
})

var _ = Describe("Job.Provides", func() {
	DescribeTable("matches check names",
		func(job workflows.Job, check string, want bool) {
			Expect(job.Provides(check)).To(Equal(want))
		},
		Entry("by id", workflows.Job{ID: "lint"}, "lint", true),
		Entry("by name", workflows.Job{ID: "t", Name: "Run tests"}, "Run tests", true),
		Entry("not by id when named", workflows.Job{ID: "t", Name: "Run tests"}, "t", false),
		Entry("regex characters are literal", workflows.Job{ID: "a.b"}, "aXb", false),
		Entry("matrix suffix", workflows.Job{ID: "build", Matrix: true}, "build (amd64)", true),
		Entry("no matrix suffix without matrix", workflows.Job{ID: "build"}, "build (amd64)", false),
		Entry("expression", workflows.Job{ID: "e", Name: "e2e (${{ matrix.pg }})", Matrix: true}, "e2e (14)", true),
		Entry("reusable", workflows.Job{ID: "release", Reusable: true}, "release / build", true),
	)
})

var _ = Describe("Set", func() {
	It("finds the workflow that provides a check", func() {
		set, err := workflows.ParseAll(map[string][]byte{
			".github/workflows/ci.yml":    []byte(ci),
			".github/workflows/title.yml": []byte(title),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(set.Find("Run unit tests").Path).To(Equal(".github/workflows/ci.yml"))
		Expect(set.Find("Run unit tests").MergeGroup).To(BeTrue())
		Expect(set.Find("Validate PR Title").MergeGroup).To(BeFalse())
		Expect(set.Find("e2e (18)")).NotTo(BeNil())
		Expect(set.Find("missing")).To(BeNil())
	})

	It("fails when a file is invalid", func() {
		_, err := workflows.ParseAll(map[string][]byte{"bad.yml": []byte("jobs: [\n")})
		Expect(err).To(HaveOccurred())
	})
})
