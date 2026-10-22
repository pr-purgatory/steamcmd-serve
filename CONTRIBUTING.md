# Contributing to steamcmd-serve

## Build, test, lint

```sh
make build      # builds bin/controller
make test       # go test ./...
make lint-api   # lints api/openapi.yaml with Redocly
```

Run `make test` and `make lint-api` before opening a pull request. CI runs the
same checks.

## Sign-off (DCO)

Every commit must carry a `Signed-off-by` line certifying the [Developer
Certificate of Origin](https://developercertificate.org/). Add it with:

```sh
git commit -s
```

## Adding a game

Each supported game is a directory under `games/<id>/` containing a
`manifest.yaml`. Manifests are validated against `games/manifest.schema.json`.
See [docs/adr/0001-architecture.md](docs/adr/0001-architecture.md) for the
architecture behind this layout.

## Releasing

Releases are cut from GitHub Actions: open **Actions > Release > Run workflow**,
pick the branch, and optionally set **tag_name**. Left empty, the tag is the UTC
date in CalVer form, `YYYY.MM.DD`, or `YYYY.MM.DD.N` when that date already has
a
release. The workflow tags the selected commit, builds and pushes the images
(`ghcr.io/pr-purgatory/steamcmd-serve`, `steamcmd-base`, `steamcmd-wine`, each
tagged with the release tag and `latest`), builds the desktop clients, and then
publishes the GitHub release with generated notes.

Pushing a tag yourself (`v1.2.3` or `2026.10.22`-style) runs the same build for
that tag. The tag logic lives in `.github/scripts/release-tag.sh` and is tested
by `.github/scripts/release-tag_test.sh`.
