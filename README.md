# ghsettings

Manage the settings of GitHub repositories from a declarative config: repository settings and a branch ruleset
(pull request reviews, required checks, merge queue), for many repositories across organizations.

It is used by [pgvillage/github-settings](https://github.com/pgvillage/github-settings) to keep the repositories of
the PgVillage organizations consistent.

ghsettings is stateless: GitHub is the state. The ruleset of a repository is found by its name, and only the fields
in the config are compared, so settings that the config doesn't mention are left alone.

## Usage

```shell
export GITHUB_TOKEN=...   # or GHSETTINGS_TOKEN_<ORG> per org, e.g. GHSETTINGS_TOKEN_PGVILLAGE_TOOLS
ghsettings plan --config config/                    # all orgs in the config
ghsettings plan -c config/ -o pgvillage-tools -r 'pg*'
```

`plan` shows what would change and changes nothing. With `--detailed-exitcode` it exits with 2 when there are
changes (for drift detection).

The token needs read access to the administration (settings, rulesets) and contents (workflows) of the repositories,
e.g. a GitHub App installation token with `Administration: read` and `Contents: read`.

## Config

The config is a YAML file, or a directory of `*.yaml` files that are merged. The desired state of a repository is
built from layers, each overriding the previous one:

```text
standard -> org -> profile -> repo rule
```

```yaml
standard:
  settings:                    # fields of the repository API (PATCH /repos/{owner}/{repo})
    allow_auto_merge: true
    allow_merge_commit: false
    allow_rebase_merge: false
    allow_squash_merge: true
    delete_branch_on_merge: true
  ruleset:
    name: main                 # default: main
    required_checks: [CI result, Validate PR Title]
    merge_queue:               # enabled where it works, see below
      merge_method: SQUASH
      grouping_strategy: ALLGREEN
    rules:                     # rule type -> parameters, sent to the API as they are
      deletion: {}
      non_fast_forward: {}
      pull_request:
        required_approving_review_count: 1
        dismiss_stale_reviews_on_push: true
        allowed_merge_methods: [squash]

profiles:
  rust:
    ruleset:
      required_checks: [CI result, Validate PR Title, cargo audit]

orgs:
  pgvillage-tools:
    settings: {}               # org-wide overrides (optional)
    repos:                     # the first rule that matches a repository applies
      - match: stolon          # glob on the repository name
        ignore: true
      - match: pg_tps_optimizer
        profile: rust
      - match: "*"
```

- Maps are merged deeply, lists and scalars are replaced, and `null` removes a key (e.g. `merge_queue: null`).
- `rules` takes any rule type and parameter of the
  [rulesets API](https://docs.github.com/en/rest/repos/rules), so new API fields need no change in ghsettings.
  Required checks and the merge queue are set with `required_checks` and `merge_queue` instead.
- Defaults: `enforcement: active`, `target: branch`, conditions: the default branch, and required checks must come
  from GitHub Actions (`checks_integration_id: 15368`).
- Repositories that no rule matches are listed by `plan`; archived repositories are skipped.

## Safety checks

- A required check is only required when a workflow job of the repository provides it (by job name or id, including
  matrix jobs and calls to reusable workflows). Otherwise every pull request would wait for it forever.
- The merge queue is only enabled on public repositories (private repositories need GitHub Enterprise for it) where
  every workflow that provides a required check has a `merge_group` trigger. Otherwise queued pull requests would
  wait for checks that never come. Elsewhere the required checks are strict (branches must be up to date) instead.

`plan` reports each of these as a warning.

## Development

```shell
make build        # bin/ghsettings
make test         # unit tests with coverage
make lint         # golangci-lint
```

## License

[GNU General Public License v3.0](LICENSE)
