# Controller image. Build context: repository root.
#   docker build -f images/controller/Dockerfile -t steamcmd-serve .

FROM node:22.23.4-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY api/openapi.yaml /src/api/openapi.yaml
# The UI imports game settings schemas (web/src/lib/api/mock.ts) from ../games.
COPY games/ /src/games/
# `build` is plain `vite build`; the committed src/lib/api/schema.d.ts is used.
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm AS go
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w" -o /out/controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot
ARG IMAGE_REPO=ghcr.io/OWNER
ARG VERSION=dev
LABEL org.opencontainers.image.title="steamcmd-serve controller" \
      org.opencontainers.image.source="https://github.com/OWNER/steamcmd-serve" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=go /out/controller /usr/local/bin/controller
COPY --from=web /src/web/dist /ui
ENV UI_DIR=/ui \
    DATA_DIR=/data \
    IMAGE_REPO=${IMAGE_REPO} \
    IMAGE_TAG=${VERSION}
EXPOSE 8080
# Compose/Unraid override with --user PUID:PGID.
USER nonroot
ENTRYPOINT ["/usr/local/bin/controller"]
