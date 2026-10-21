# Contributing to steamcmd-serve

## Build, test, lint

```sh
make build      # builds bin/controller
make test       # go test ./...
make lint-api   # lints api/openapi.yaml with Redocly
```

Run `make test` and `make lint-api` before opening a pull request. CI runs the same checks.

## Sign-off (DCO)

Every commit must carry a `Signed-off-by` line certifying the [Developer Certificate of Origin](https://developercertificate.org/). Add it with:

```sh
git commit -s
```

## Adding a game

Each supported game is a directory under `games/<id>/` containing a `manifest.yaml`. Manifests are validated against `games/manifest.schema.json`. See [docs/adr/0001-architecture.md](docs/adr/0001-architecture.md) for the architecture behind this layout.
