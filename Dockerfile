# The toolchain for working on gantry itself (bin/go): Go, templ and sqlc.
# An app's Dockerfile has the same stage, then its dev, test and production.
FROM golang:1.27-bookworm AS toolchain
COPY --from=sqlc/sqlc:1.31.1 /workspace/sqlc /usr/local/bin/sqlc
ENV CGO_ENABLED=0 GOFLAGS="-buildvcs=false -tags=nodynamic"
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
WORKDIR /src
