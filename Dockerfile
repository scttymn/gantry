# The toolchain for working on gantry itself (bin/go): Go, templ and sqlc.
# An app's Dockerfile has the same stage, then its dev, test and production.
FROM golang:1.27-bookworm AS toolchain
COPY --from=sqlc/sqlc:1.31.1 /workspace/sqlc /usr/local/bin/sqlc
ENV CGO_ENABLED=0 GOFLAGS="-buildvcs=false -tags=nodynamic"
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
WORKDIR /src

# Release CLIs. bin/gantry downloads gantry-$os-$arch from the GitHub Release
# and checks it against SHA256SUMS. The workflow exports this stage.
FROM golang:1.27-bookworm AS release-build
ENV CGO_ENABLED=0 GOFLAGS="-buildvcs=false"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /dist && for os in darwin linux; do for arch in arm64 amd64; do \
      GOOS=$os GOARCH=$arch go build -trimpath \
        -o /dist/gantry-$os-$arch ./cmd/gantry || exit 1; \
    done; done

FROM scratch AS release
COPY --from=release-build /dist/ /
