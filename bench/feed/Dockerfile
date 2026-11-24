FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS toolchain
COPY --from=sqlc/sqlc:1.31.1@sha256:70f53171d27b2424e9358869975455a6e955a5aa8e58a998a270a6e34e525537 /workspace/sqlc /usr/local/bin/sqlc
RUN apt-get update && apt-get install -y --no-install-recommends sqlite3 && rm -rf /var/lib/apt/lists/*
ENV CGO_ENABLED=1 CGO_CFLAGS="-O2 -g -DSQLITE_SOUNDEX -DSQLITE_LIKE_DOESNT_MATCH_BLOBS -DSQLITE_ENABLE_OFFSET_SQL_FUNC -DSQLITE_ENABLE_DBPAGE_VTAB -DSQLITE_ENABLE_GEOPOLY" \
    GOFLAGS="-buildvcs=false -tags=nodynamic,timetzdata,netgo,osusergo,sqlite_omit_load_extension,sqlite_fts5,sqlite_math_functions,sqlite_dbstat,sqlite_stat4"
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1118
WORKDIR /app

FROM toolchain AS dev
ENV GANTRY_ENV=development ADDR=:8080 DATA_DIR=/data GOMODCACHE=/app/.cache/go/mod GOCACHE=/app/.cache/go/cache
CMD ["templ", "generate", "--watch", "-path", "/app", "--watch-pattern=(.+\\.go$)|(.+\\.templ$)|(.+\\.js$)|(.+\\.css$)|(.+\\.sql$)", "--cmd=go run ./cmd/feed"]

FROM toolchain AS test
ENV GANTRY_ENV=test
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN templ generate -path /app
RUN --mount=type=cache,id=feed-assets,target=/cache/assets,sharing=locked \
    go run ./cmd/feed assets /cache/assets && cp -a /cache/assets/. assets/built/
CMD ["sh", "-c", "go vet ./... && GOFLAGS=\"$GOFLAGS,nodevcheck\" go build ./... && go test ./..."]

FROM toolchain AS build
ENV GOFLAGS="${GOFLAGS},nodevcheck"
COPY go.mod go.sum ./
RUN go mod download
COPY . .
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
