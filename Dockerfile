# The toolchain for working on gantry itself (bin/go): Go, templ and sqlc.
# An app's Dockerfile has the same stage, then its dev, test and production.
# Images are pinned by digest; internal/pins has them, and its test holds
# these copies to it.
FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS toolchain
COPY --from=sqlc/sqlc:1.31.1@sha256:70f53171d27b2424e9358869975455a6e955a5aa8e58a998a270a6e34e525537 /workspace/sqlc /usr/local/bin/sqlc
# Match generated Postgres apps: integration tests dump and compare schemas.
RUN apt-get update && apt-get install -y --no-install-recommends postgresql-common \
 && /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y \
 && apt-get install -y --no-install-recommends postgresql-client-17 \
 && rm -rf /var/lib/apt/lists/*
ENV CGO_ENABLED=0 GOFLAGS="-buildvcs=false -tags=nodynamic"
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1118
WORKDIR /src

# Release CLIs. bin/gantry downloads gantry-$os-$arch from the GitHub Release
# and checks it against SHA256SUMS. The workflow exports this stage.
FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS release-build
# The release's tag, which the binaries say they are (main.releaseVersion): gantry
# new writes it into an app's go.mod, and bin/gantry downloads that release.
ARG VERSION
ENV CGO_ENABLED=0 GOFLAGS="-buildvcs=false"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /dist && for os in darwin linux; do for arch in arm64 amd64; do \
      GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-X main.releaseVersion=$VERSION" \
        -o /dist/gantry-$os-$arch ./cmd/gantry || exit 1; \
    done; done

FROM scratch AS release
COPY --from=release-build /dist/ /
