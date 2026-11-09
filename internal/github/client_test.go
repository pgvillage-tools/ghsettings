// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package github_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgvillage-tools/ghsettings/internal/github"
)

var _ = Describe("Client", func() {
	var (
		server *httptest.Server
		client *github.Client
		ctx    = context.Background()
	)

	BeforeEach(func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/orgs/o/repos", func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer secret"))
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[{"name":"b","archived":true}]`)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/orgs/o/repos?page=2>; rel="next"`, server.URL))
			fmt.Fprint(w, `[{"name":"a","visibility":"public"}]`)
		})
		mux.HandleFunc("/repos/o/a", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"name":"a","allow_auto_merge":true}`)
		})
		mux.HandleFunc("/repos/o/a/rulesets", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{"id":1,"name":"main","source_type":"Organization"},
				{"id":2,"name":"other","source_type":"Repository"},
				{"id":3,"name":"main","source_type":"Repository"}]`)
		})
		mux.HandleFunc("/repos/o/a/rulesets/3", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"id":3,"name":"main","rules":[]}`)
		})
		mux.HandleFunc("/repos/o/a/contents/.github/workflows", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{"type":"file","path":".github/workflows/ci.yml"},
				{"type":"file","path":".github/workflows/README.md"},
				{"type":"dir","path":".github/workflows/sub"}]`)
		})
		mux.HandleFunc("/repos/o/a/contents/.github/workflows/ci.yml", func(w http.ResponseWriter, _ *http.Request) {
			enc := base64.StdEncoding.EncodeToString([]byte("on: push\n"))
			fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":"%s\n"}`, enc)
		})
		mux.HandleFunc("/repos/o/broken", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		})
		mux.HandleFunc("/repos/o/garbage", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{not json`)
		})
		server = httptest.NewServer(mux)
		client = github.NewClient("secret")
		client.BaseURL = server.URL
	})

	AfterEach(func() { server.Close() })

	It("follows pagination", func() {
		repos, err := client.ListRepos(ctx, "o")
		Expect(err).NotTo(HaveOccurred())
		Expect(repos).To(Equal([]github.RepoSummary{{Name: "a", Visibility: "public"}, {Name: "b", Archived: true}}))
	})

	It("gets a repository as a map", func() {
		repo, err := client.GetRepo(ctx, "o", "a")
		Expect(err).NotTo(HaveOccurred())
		Expect(repo).To(HaveKeyWithValue("allow_auto_merge", true))
	})

	It("finds the repository ruleset by name", func() {
		rs, err := client.FindRuleset(ctx, "o", "a", "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(rs).To(HaveKeyWithValue("id", BeEquivalentTo(3)))
	})

	It("returns nil when there is no such ruleset", func() {
		rs, err := client.FindRuleset(ctx, "o", "a", "missing")
		Expect(err).NotTo(HaveOccurred())
		Expect(rs).To(BeNil())
	})

	It("reads the workflow files", func() {
		files, err := client.ListWorkflows(ctx, "o", "a")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(Equal(map[string][]byte{".github/workflows/ci.yml": []byte("on: push\n")}))
	})

	It("returns no workflows when the directory is missing", func() {
		files, err := client.ListWorkflows(ctx, "o", "none")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(BeEmpty())
	})

	It("returns ErrNotFound on 404", func() {
		_, err := client.GetRepo(ctx, "o", "missing")
		Expect(err).To(MatchError(github.ErrNotFound))
	})

	It("returns the API message on errors", func() {
		_, err := client.GetRepo(ctx, "o", "broken")
		Expect(err).To(MatchError(ContainSubstring("Bad credentials")))
		_, err = client.FindRuleset(ctx, "o", "broken", "main")
		Expect(err).To(HaveOccurred())
	})

	It("fails on invalid JSON", func() {
		_, err := client.GetRepo(ctx, "o", "garbage")
		Expect(err).To(MatchError(ContainSubstring("decoding response")))
	})
})

var _ = Describe("Client writes", func() {
	var (
		server *httptest.Server
		client *github.Client
		got    []string
		ctx    = context.Background()
	)

	BeforeEach(func() {
		got = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			got = append(got, fmt.Sprintf("%s %s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"), body))
			switch {
			case r.URL.Path == "/repos/o/denied":
				http.Error(w, `{"message":"Resource not accessible"}`, http.StatusForbidden)
			case r.Method == http.MethodPost:
				fmt.Fprint(w, `{"id":7,"name":"main"}`)
			default:
				fmt.Fprint(w, `{}`)
			}
		}))
		client = github.NewClient("secret")
		client.BaseURL = server.URL
	})

	AfterEach(func() { server.Close() })

	It("patches a repository", func() {
		Expect(client.UpdateRepo(ctx, "o", "a", map[string]any{"allow_auto_merge": true})).To(Succeed())
		Expect(got).To(Equal([]string{`PATCH /repos/o/a application/json {"allow_auto_merge":true}`}))
	})

	It("creates a ruleset", func() {
		id, err := client.CreateRuleset(ctx, "o", "a", map[string]any{"name": "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(id).To(BeEquivalentTo(7))
		Expect(got).To(Equal([]string{`POST /repos/o/a/rulesets application/json {"name":"main"}`}))
	})

	It("updates a ruleset", func() {
		Expect(client.UpdateRuleset(ctx, "o", "a", 7, map[string]any{"name": "main"})).To(Succeed())
		Expect(got).To(Equal([]string{`PUT /repos/o/a/rulesets/7 application/json {"name":"main"}`}))
	})

	It("returns errors", func() {
		Expect(client.UpdateRepo(ctx, "o", "denied", map[string]any{})).To(MatchError(ContainSubstring("403")))
		Expect(client.UpdateRepo(ctx, "o", "a", map[string]any{"bad": make(chan int)})).To(HaveOccurred())
	})
})
