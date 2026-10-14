#!/usr/bin/env bash
# The $12 server challenge's scoring, as its README has it, of one submission:
#
#   score.sh CHALLENGE_DIR SUBMISSION_NAME [SEARCH_TOP]
#
# The submission is built by its own install.sh and build.sh on Ubuntu 24.04 and
# started by its start.sh, on one core with 2 GB and no swap, 65,535 descriptors,
# serving directly on port 80, on a fresh copy of seed/feed.db for every run.
# The load is bench/load.js, unchanged, under k6 on the other cores: every user
# its own connection.
#
#   1. warm-up: 1,000 users for 2 minutes (not scored)
#   2. the limit: from 2,500 users, doubling until a run fails, then narrowing
#      (these runs hold for 60 s; to within NARROW users, 1,000 unless set)
#   3. the score: a 5-minute hold at that number; if it fails, 250 fewer, again
#
# A run passes on k6's own thresholds in load.js: p95 under 500 ms, p99 under
# 1 s, under 1% failed.
set -uo pipefail
challenge=$(cd "$1" && pwd); name=$2; top=${3:-50000}; narrow=${NARROW:-1000}
net=challenge-score sut=challenge-sut image="challenge-$name"
cores=$(docker info --format '{{.NCPU}}'); load_cpus="1-$((cores - 1))"; [ "$cores" -gt 4 ] && load_cpus="2-$((cores - 1))"
out=${OUT:-$PWD/score-$name}; mkdir -p "$out"; : > "$out/runs.md"
log() { echo "$@" >&2; }

log "building $name by its install.sh and build.sh"
docker build -q -t "$image" -f - "$challenge/submissions/$name" >/dev/null <<'DOCKERFILE' || { log "the build failed"; exit 1; }
FROM ubuntu:24.04
COPY . /sub
RUN bash /sub/install.sh
RUN bash /sub/build.sh
CMD ["bash", "/sub/start.sh"]
DOCKERFILE
docker network inspect "$net" >/dev/null 2>&1 || docker network create "$net" >/dev/null
cleanup() { docker rm -f "$sut" challenge-k6 >/dev/null 2>&1; docker network rm "$net" >/dev/null 2>&1; }
trap cleanup EXIT

start() { # a fresh copy of the seed, and the submission on it
	docker rm -f "$sut" >/dev/null 2>&1
	docker run -d --name "$sut" --network "$net" --cpuset-cpus=0 --memory=2g --memory-swap=2g --ulimit nofile=65535:65535 \
		-v "$challenge/seed:/seed:ro" -e SQLITE_PATH=/data/feed.db -e JWT_SECRET=twelve-dollar-challenge -e HOST=0.0.0.0 -e PORT=80 \
		"$image" bash -c 'mkdir -p /data && cp /seed/feed.db /data/feed.db && exec bash /sub/start.sh' >/dev/null
	for _ in $(seq 60); do
		docker run --rm --network "$net" curlimages/curl:8.10.1 -sf -o /dev/null "http://$sut/health" 2>/dev/null && return 0
		sleep 1
	done
	log "no healthy /health in 60 s"; docker logs --tail 20 "$sut" >&2; return 1
}

run() { # run USERS RAMP HOLD LABEL: 0 when k6's thresholds pass
	local users=$1 ramp=$2 hold=$3 label=$4 status=0
	start || return 2
	(sleep $(( ${ramp%s} + 20 )); docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' "$sut" > "$out/stats.txt" 2>/dev/null) &
	docker run --rm --name challenge-k6 --network "$net" --cpuset-cpus="$load_cpus" --ulimit nofile=250000:250000 \
		--sysctl net.ipv4.ip_local_port_range="1024 65535" -v "$challenge:/challenge:ro" -w /challenge grafana/k6:1.3.0 \
		run --quiet -e BASE_URL="http://$sut" -e VUS="$users" -e RAMP_UP="$ramp" -e DURATION="$hold" bench/load.js > "$out/k6.log" 2>&1 || status=$?
	wait
	local p95 p99 failed reqs oom verdict=passes
	p95=$(grep -oE "p\(95\)=[0-9.]+[a-zµ]+" "$out/k6.log" | head -1 | cut -d= -f2)
	p99=$(grep -oE "p\(99\)=[0-9.]+[a-zµ]+" "$out/k6.log" | head -1 | cut -d= -f2)
	failed=$(grep -E "http_req_failed" "$out/k6.log" | grep -oE "[0-9.]+%" | head -1)
	reqs=$(grep -E "http_reqs" "$out/k6.log" | grep -oE "[0-9.]+/s" | head -1)
	oom=$(docker inspect -f '{{.State.OOMKilled}} {{.State.Running}}' "$sut" 2>/dev/null)
	[ "$status" = 0 ] || verdict="fails"
	case "$oom" in "true "*) verdict="fails: out of memory";; *" false") verdict="fails: the server died";; esac
	echo "| $label | $users | ${p95:-?} | ${p99:-?} | ${failed:-?} | ${reqs:-?} | $(cat "$out/stats.txt" 2>/dev/null | sed 's/ \/ 2GiB//') | $verdict |" | tee -a "$out/runs.md" >&2
	[ "$verdict" = passes ]
}

echo "| Run | Users | p95 | p99 | Failed | Requests | Server CPU, memory | |" >> "$out/runs.md"
echo "| --- | ---: | ---: | ---: | ---: | ---: | --- | --- |" >> "$out/runs.md"
log "| Run | Users | p95 | p99 | Failed | Requests | Server CPU, memory | |"
run 1000 60s 2m "warm-up (not scored)" || true

passed=0 failed=0 users=2500
while [ "$failed" = 0 ]; do
	if run "$users" 30s 60s "doubling"; then
		passed=$users
		[ "$users" -ge "$top" ] && break
		users=$(( users * 2 )); [ "$users" -gt "$top" ] && users=$top
	else
		failed=$users
	fi
done
if [ "$passed" = 0 ]; then echo "SCORE $name: under 2,500" | tee "$out/score.txt"; exit 0; fi
if [ "$failed" != 0 ]; then
	while [ $(( failed - passed )) -gt "$narrow" ]; do
		users=$(( (passed + failed) / 2 / 250 * 250 ))
		if run "$users" 30s 60s "narrowing"; then passed=$users; else failed=$users; fi
	done
fi
users=$passed
for attempt in 1 2 3 4 5 6; do
	if run "$users" 60s 5m "5-minute hold"; then
		note=""; [ "$failed" = 0 ] && note=" (at least: $top was the most tried)"
		echo "SCORE $name: $users users$note" | tee "$out/score.txt"
		docker cp "$sut:/data/feed.db" "$out/feed.db" >/dev/null 2>&1 && echo "tables after the hold: $(sqlite3 "$out/feed.db" .tables 2>/dev/null | tr -s ' \n' ' ')" | tee -a "$out/score.txt"; rm -f "$out/feed.db"
		exit 0
	fi
	users=$(( users - 250 ))
done
echo "SCORE $name: no 5-minute hold passed from $passed down to $(( users + 250 ))" | tee "$out/score.txt"
