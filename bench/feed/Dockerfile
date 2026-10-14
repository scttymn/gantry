# Stages: toolchain (Go, templ, sqlc, sqlite3), dev (the mounted code,
# rebuilt on change), test, and production (a static binary on an empty
# image). Houston builds dev for gantry dev, test for gantry test, and
# production to deploy.

FROM golang:1.27-bookworm AS toolchain
COPY --from=sqlc/sqlc:1.31.1 /workspace/sqlc /usr/local/bin/sqlc
# sqlite3, for db console.
RUN apt-get update && apt-get install -y --no-install-recommends sqlite3 && rm -rf /var/lib/apt/lists/*
# cgo: gantry's db has SQLite compiled from C (the production build links
# it statically, so the binary still runs on an empty image). The sqlite_
# tags and CGO_CFLAGS are what that C is compiled with: full-text search,
# math functions and the rest SQLite translated to Go has, so an app's SQL
# means the same either way (gantry's db won't start without them).
# nodynamic: gantry's image codecs run as WASM. netgo, osusergo and
# timetzdata: Go's own name lookups and time zones, not the C library's, as
# a static binary on an empty image needs.
ENV CGO_ENABLED=1 CGO_CFLAGS="-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY" \
    GOFLAGS="-buildvcs=false -tags=nodynamic,timetzdata,netgo,osusergo,sqlite_omit_load_extension,sqlite_fts5,sqlite_math_functions,sqlite_dbstat,sqlite_stat4"
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
WORKDIR /app

# templ regenerates on change and restarts the server. Caches live in the
# mounted folder's .cache/ (ignored, and skipped by the watcher), so
# restarts reuse them.
FROM toolchain AS dev
ENV GANTRY_ENV=development ADDR=:8080 DATA_DIR=/data GOMODCACHE=/app/.cache/go/mod GOCACHE=/app/.cache/go/cache
CMD ["templ", "generate", "--watch", "-path", "/app", "--watch-pattern=(.+\\.go$)|(.+\\.templ$)|(.+\\.js$)|(.+\\.css$)|(.+\\.sql$)", "--cmd=go run ./cmd/feed"]

FROM toolchain AS test
ENV GANTRY_ENV=test
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN templ generate -path /app
# The assets precompiled as the build does them, so the tests see what
# production serves and no picture is resized on request.
RUN --mount=type=cache,id=feed-assets,target=/cache/assets,sharing=locked \
    go run ./cmd/feed assets /cache/assets && cp -a /cache/assets/. assets/built/
CMD ["sh", "-c", "go vet ./... && go test ./..."]

FROM toolchain AS build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The assets precompiled first (Rails' assets:precompile: the scripts
# minified, the pictures' copies), for go build to embed. They're kept in a
# build cache between deploys, so only a new or changed picture is resized;
# and so is Go's, so SQLite's C is compiled once, not on every deploy. The
# binary is linked statically, for the empty image it runs on.
RUN --mount=type=cache,id=feed-assets,target=/cache/assets,sharing=locked \
    --mount=type=cache,id=feed-go,target=/root/.cache/go-build \
    templ generate -path /app && go run ./cmd/feed assets /cache/assets && cp -a /cache/assets/. assets/built/ \
 && go build -trimpath -ldflags "-s -w -linkmode external -extldflags -static" -o /out/app ./cmd/feed
RUN mkdir -p /out/data

FROM scratch AS production
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/app /app
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV ADDR=:8080 DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/app"]
