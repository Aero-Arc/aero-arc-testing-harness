# Contributing

Changes are welcome when they make a safety claim more precise, make a failure
reproducible, or make the evidence easier to understand. Start with
[Adding tests](docs/ADDING_TESTS.md) for the shortest path from a federation
story to an executable scenario.

## Developer Certificate of Origin

This repository uses the [Developer Certificate of Origin 1.1](https://developercertificate.org/).
By adding a `Signed-off-by` trailer, you certify that you have the right to
submit the contribution under this repository's license and agree to the DCO.

Configure Git with your real contributor identity, then sign every commit:

```bash
git config user.name "Your Name"
git config user.email "you@example.com"
git commit --signoff
```

The resulting commit message must end with a trailer matching the commit author
or committer:

```text
Signed-off-by: Your Name <you@example.com>
```

The pull-request workflow checks every commit, not only the tip. To repair the
latest local commit, use `git commit --amend --signoff`. To repair every commit
on a private feature branch based on `origin/main`, use:

```bash
git rebase --signoff origin/main
./scripts/check-dco.sh origin/main HEAD
git push --force-with-lease
```

Do not rewrite a shared branch without coordinating with everyone using it, and
never add a sign-off on another person's behalf.

## Before opening a pull request

Run the fast validation:

```bash
gofmt -w ./cmd ./internal ./e2e
go test ./...
go vet ./...
git diff --check
./scripts/check-dco.sh origin/main HEAD
```

Run `./scripts/run-e2e.sh` when the change affects the Docker topology, fixture,
fault injection, invariant probes, or executable federation scenarios. Include
the named invariant, bounded fault, observed authorities, and validation result
in the pull-request description.
