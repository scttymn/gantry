---- MODULE Jobs ----
\* One gantry_jobs row and the processes that run it, as jobs/run.go and
\* jobs/jobs.go have it: Run's workers (Slots a process, sharing its
\* claimed_by), its heartbeat (beat, retried settles, its own orphaned claims
\* put back, then maintenance), maintenance's crash recovery, shutdown with
\* its grace, crashes, failed and ambiguous commits, and Retry.
\*
\* Each Mut* constant puts one fix back the way the code was before it, and
\* spec/Jobs.<Mut>.cfg checks that the invariant guarding it then fails:
\*   MutNoCrashCount        B1: a dead process's claim goes back forever
\*   MutBeatStopsOnShutdown B2: the heartbeat stops when Run's ctx ends
\*   MutNoSettleRetry       B3: a settle that errors is logged and dropped
\*   MutFoundBeforeCommit   B6: a claim whose commit failed runs anyway
\*   MutNoOrphanRelease     B3/B6: no heartbeat puts back this process's own
\*                          claims that no worker runs
\*   MutRetryFenceClaimed   a retried settle fences on state = 'claimed' only,
\*                          so one that errored but went through looks lost
\* Reach* are witnesses, each violated by its spec/Jobs.Reach*.cfg, so the
\* main run's pass covers the states that matter.
\*
\* Assumptions: one discrete clock for every host; every live process beats
\* within HB (< Dead) and puts back an orphaned claim at its next beat; Grace
\* is under one tick (5 s against Heartbeat's 60 s); limits, discards and
\* Drain are left out (TestLimit*, TestSettleFails). The row may have
\* crashed before the model starts (crashes in Init), so one crash reaches
\* both the put-back and the failure.
\*
\* A longer run, not in CI: three processes of one slot, two crashes from a
\* clean row (MaxTime 7). It passed with 32,616,691 distinct states in 85 s on
\* 24 cores:
\*   docker run --rm -v "$PWD/spec:/spec:ro" -v ~/.cache/gantry:/cache:ro <bin/tlc's image> sh -c '
\*     sed -e "s/MaxTime = 5/MaxTime = 7/" -e "s/Procs = {a, b}/Procs = {a, b, c}/" \
\*       -e "s/Slots = {1, 2}/Slots = {1}/" /spec/Jobs.cfg >/tmp/Long.cfg && cp /spec/Jobs.tla /tmp &&
\*     java -cp /cache/tla2tools-v1.7.4.jar tlc2.TLC -deadlock -workers auto -config /tmp/Long.cfg /tmp/Jobs.tla'
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Procs, Slots, MaxAttempts, MaxCrashes, Dead, HB, MaxTime,
          Crashes, CommitErrors, SettleErrors,
          MutNoCrashCount, MutBeatStopsOnShutdown, MutNoSettleRetry,
          MutFoundBeforeCommit, MutNoOrphanRelease, MutRetryFenceClaimed

None == "none"
W == Procs \X Slots
Kinds == {"finished", "putback", "retry", "failed"}

VARIABLES st, by, at, att, runAt, crashes, \* the row (at = 0 is NULL; times start at 1)
          proc, row, hb,                   \* processes, gantry_job_processes
          pc, wAt, wAtt, wRes,             \* each worker slot's claim in memory
          fly, pend,                       \* each Run's tracker: claims in flight, settles to retry
          now,
          starts, putBacks, reclaims, exhausted, okDone, rerunAfterOk, okLost, retried \* history

rowVars == <<st, by, at, att, runAt, crashes>>
procVars == <<proc, row, hb>>
wVars == <<pc, wAt, wAtt, wRes>>
history == <<starts, putBacks, reclaims, exhausted, okDone, rerunAfterOk, okLost, retried>>
vars == <<rowVars, procVars, wVars, fly, pend, now, history>>

TypeOK ==
    /\ st \in {"ready", "claimed", "finished", "failed"}
    /\ by \in Procs \cup {None}
    /\ at \in 0..MaxTime /\ runAt \in 0..(MaxTime + 1)
    /\ proc \in [Procs -> {"new", "up", "stopping", "graceOver", "gone"}]
    /\ pc \in [W -> {"idle", "perform", "settle"}]
    /\ fly \in [Procs -> SUBSET (1..MaxTime)]
    /\ \A p \in Procs : \A r \in pend[p] : r.kind \in Kinds
    /\ now \in 1..MaxTime

Init ==
    /\ st = "ready" /\ by = None /\ at = 0 /\ att = 0 /\ runAt = 1 /\ crashes \in 0..(MaxCrashes - 1)
    /\ proc = [p \in Procs |-> "new"] /\ row = [p \in Procs |-> FALSE] /\ hb = [p \in Procs |-> 0]
    /\ pc = [w \in W |-> "idle"] /\ wAt = [w \in W |-> 0] /\ wAtt = [w \in W |-> 0] /\ wRes = [w \in W |-> "none"]
    /\ fly = [p \in Procs |-> {}] /\ pend = [p \in Procs |-> {}]
    /\ now = 1
    /\ starts = 0 /\ putBacks = 0 /\ reclaims = 0 /\ exhausted = 0
    /\ okDone = FALSE /\ rerunAfterOk = FALSE /\ okLost = FALSE /\ retried = FALSE

Live(p) == proc[p] \in {"up", "stopping", "graceOver"}
\* Run's heartbeat goes on until every job has returned (B2's fix).
Beating(p) == IF MutBeatStopsOnShutdown THEN proc[p] = "up" ELSE Live(p)

\* Run: beat on start.
Start(p) ==
    /\ proc[p] = "new"
    /\ proc' = [proc EXCEPT ![p] = "up"] /\ row' = [row EXCEPT ![p] = TRUE] /\ hb' = [hb EXCEPT ![p] = now]
    /\ UNCHANGED <<rowVars, wVars, fly, pend, now, history>>

\* claim's one transaction: the SELECT of a ready, due job and its UPDATE.
\* claimed_at is to the microsecond, so a process never makes two claims at
\* one instant: none of its claims in flight has this one's time.
CanClaim(w) == proc[w[1]] = "up" /\ pc[w] = "idle" /\ st = "ready" /\ runAt <= now /\ now \notin fly[w[1]]

StartRun(w) ==
    /\ pc' = [pc EXCEPT ![w] = "perform"] /\ wAt' = [wAt EXCEPT ![w] = now]
    /\ wAtt' = [wAtt EXCEPT ![w] = att + 1] /\ wRes' = [wRes EXCEPT ![w] = "none"]
    /\ fly' = [fly EXCEPT ![w[1]] = @ \cup {now}]
    /\ starts' = starts + 1 /\ rerunAfterOk' = (rerunAfterOk \/ okDone)

NotRun == UNCHANGED <<wVars, fly, starts, rerunAfterOk>>

Claim(w) ==
    /\ CanClaim(w)
    /\ st' = "claimed" /\ by' = w[1] /\ at' = now /\ att' = att + 1
    /\ StartRun(w)
    /\ UNCHANGED <<runAt, crashes, procVars, pend, now, putBacks, reclaims, exhausted, okDone, okLost, retried>>

\* The commit failed and saved nothing: claim returns the error and no job
\* (it registered the claim and takes it back).
ClaimCommitFails(w) ==
    /\ CommitErrors /\ CanClaim(w)
    /\ IF MutFoundBeforeCommit THEN StartRun(w) ELSE NotRun
    /\ UNCHANGED <<rowVars, procVars, pend, now, putBacks, reclaims, exhausted, okDone, okLost, retried>>

\* The commit errored but went through (Postgres): the row is claimed by this
\* process, and claim returns the error and no job.
ClaimCommitAmbiguous(w) ==
    /\ CommitErrors /\ CanClaim(w)
    /\ st' = "claimed" /\ by' = w[1] /\ at' = now /\ att' = att + 1
    /\ IF MutFoundBeforeCommit THEN StartRun(w) ELSE NotRun
    /\ UNCHANGED <<runAt, crashes, procVars, pend, now, putBacks, reclaims, exhausted, okDone, okLost, retried>>

\* perform returns; it may have ignored its context.
Return(w, r) ==
    /\ pc[w] = "perform"
    /\ pc' = [pc EXCEPT ![w] = "settle"] /\ wRes' = [wRes EXCEPT ![w] = r]
    /\ okDone' = (okDone \/ r = "ok")
    /\ UNCHANGED <<rowVars, procVars, wAt, wAtt, fly, pend, now, starts, putBacks, reclaims, exhausted, rerunAfterOk, okLost, retried>>

\* execute's choice of settle.
Kind(w) == IF wRes[w] = "ok" THEN "finished"
           ELSE IF proc[w[1]] = "graceOver" THEN "putback"
           ELSE IF wAtt[w] < MaxAttempts THEN "retry"
           ELSE "failed"
Target(k) == IF k \in {"finished", "failed"} THEN k ELSE "ready"

\* settle's UPDATE: WHERE state IN ('claimed', target) AND claimed_by, claimed_at.
Fence(p, a, k, strict) == by = p /\ at = a /\ (st = "claimed" \/ (~strict /\ st = Target(k)))

\* The settle's SET, made at t; hook: OnExhausted runs (the settle reported saved).
Apply(k, t, hook) ==
    CASE k = "finished" -> st' = "finished" /\ UNCHANGED <<by, at, att, runAt, putBacks, exhausted>>
      [] k = "putback" -> /\ st' = "ready" /\ att' = att - 1 /\ by' = None /\ at' = 0
                          /\ putBacks' = putBacks + 1 /\ UNCHANGED <<runAt, exhausted>>
      [] k = "retry" -> /\ st' = "ready" /\ runAt' = t + 1 /\ by' = None /\ at' = 0
                        /\ UNCHANGED <<att, putBacks, exhausted>>
      [] k = "failed" -> /\ st' = "failed" /\ exhausted' = exhausted + (IF hook THEN 1 ELSE 0)
                         /\ UNCHANGED <<by, at, att, runAt, putBacks>>
NoChange == UNCHANGED <<st, by, at, att, runAt, putBacks, exhausted>>

Idle(w) == /\ pc' = [pc EXCEPT ![w] = "idle"] /\ wAt' = [wAt EXCEPT ![w] = 0]
           /\ wAtt' = [wAtt EXCEPT ![w] = 0] /\ wRes' = [wRes EXCEPT ![w] = "none"]

\* The settle is saved (or matches nothing): its claim leaves the tracker,
\* compared by (id, claimed_at).
Settle(w) ==
    /\ pc[w] = "settle"
    /\ IF Fence(w[1], wAt[w], Kind(w), FALSE) THEN Apply(Kind(w), now, TRUE) ELSE NoChange
    /\ fly' = [fly EXCEPT ![w[1]] = @ \ {wAt[w]}]
    /\ Idle(w)
    /\ UNCHANGED <<crashes, procVars, pend, now, starts, reclaims, okDone, rerunAfterOk, okLost, retried>>

\* The settle errored: Run keeps it, its claim still in flight, for the
\* heartbeat to try again; the worker's slot is free.
Held(w) ==
    IF MutNoSettleRetry
    THEN /\ fly' = [fly EXCEPT ![w[1]] = @ \ {wAt[w]}] /\ UNCHANGED pend
    ELSE /\ pend' = [pend EXCEPT ![w[1]] = @ \cup {[at |-> wAt[w], kind |-> Kind(w), t |-> IF Kind(w) = "retry" THEN now ELSE 0]}]
         /\ UNCHANGED fly

SettleFails(w) ==
    /\ SettleErrors /\ pc[w] = "settle"
    /\ NoChange /\ Held(w) /\ Idle(w)
    /\ UNCHANGED <<crashes, procVars, now, starts, reclaims, okDone, rerunAfterOk, okLost, retried>>

\* The settle errored but went through (Postgres): OnExhausted waits for the retry.
SettleAmbiguous(w) ==
    /\ SettleErrors /\ pc[w] = "settle"
    /\ IF Fence(w[1], wAt[w], Kind(w), FALSE) THEN Apply(Kind(w), now, FALSE) ELSE NoChange
    /\ Held(w) /\ Idle(w)
    /\ UNCHANGED <<crashes, procVars, now, starts, reclaims, okDone, rerunAfterOk, okLost, retried>>

\* A heartbeat (or the last try as Run returns) saves a kept settle. With its
\* fence widened to the target state, one that went through counts as saved.
Resettle(p, r) ==
    /\ Live(p) /\ r \in pend[p]
    /\ IF Fence(p, r.at, r.kind, MutRetryFenceClaimed) THEN Apply(r.kind, r.t, TRUE) ELSE NoChange
    /\ pend' = [pend EXCEPT ![p] = @ \ {r}] /\ fly' = [fly EXCEPT ![p] = @ \ {r.at}]
    /\ UNCHANGED <<crashes, procVars, wVars, now, starts, reclaims, okDone, rerunAfterOk, okLost, retried>>

\* A heartbeat puts back this process's claims older than HB that it has no
\* record of (a claim whose commit errored but went through).
Orphaned(p) == st = "claimed" /\ by = p /\ at + HB < now /\ at \notin fly[p]
Release(p) ==
    /\ ~MutNoOrphanRelease /\ Beating(p) /\ Orphaned(p)
    /\ st' = "ready" /\ att' = att - 1 /\ by' = None /\ at' = 0
    /\ UNCHANGED <<runAt, crashes, procVars, wVars, fly, pend, now, history>>

Beat(p) ==
    /\ Beating(p)
    /\ row' = [row EXCEPT ![p] = TRUE] /\ hb' = [hb EXCEPT ![p] = now]
    /\ UNCHANGED <<rowVars, proc, wVars, fly, pend, now, history>>

\* maintain, one transaction, only while Run's ctx lasts: delete silent
\* processes, then fail the claims of dead ones on their MaxCrashes-th crash
\* and put the rest back, both with attempts - 1 and crashes + 1.
Alive2 == [q \in Procs |-> row[q] /\ hb[q] + Dead >= now]
Orphan2 == st = "claimed" /\ ~Alive2[by] /\ at + Dead < now
Maintain(p) ==
    /\ proc[p] = "up" /\ ~Orphan2
    /\ row' = Alive2
    /\ UNCHANGED <<rowVars, proc, hb, wVars, fly, pend, now, history>>
MaintainReclaim(p) ==
    /\ proc[p] = "up" /\ Orphan2 /\ (MutNoCrashCount \/ crashes + 1 < MaxCrashes)
    /\ row' = Alive2
    /\ st' = "ready" /\ att' = att - 1 /\ crashes' = crashes + 1 /\ by' = None /\ at' = 0
    /\ reclaims' = reclaims + 1
    /\ UNCHANGED <<runAt, proc, hb, wVars, fly, pend, now, starts, putBacks, exhausted, okDone, rerunAfterOk, okLost, retried>>
MaintainCrashFail(p) ==
    /\ proc[p] = "up" /\ Orphan2 /\ ~MutNoCrashCount /\ crashes + 1 >= MaxCrashes
    /\ row' = Alive2
    /\ st' = "failed" /\ att' = att - 1 /\ crashes' = crashes + 1 /\ exhausted' = exhausted + 1
    /\ UNCHANGED <<by, at, runAt, proc, hb, wVars, fly, pend, now, starts, putBacks, reclaims, okDone, rerunAfterOk, okLost, retried>>

\* Run's ctx ends: no more claims, ticks or maintenance.
Shutdown(p) ==
    /\ proc[p] = "up" /\ proc' = [proc EXCEPT ![p] = "stopping"]
    /\ UNCHANGED <<rowVars, row, hb, wVars, fly, pend, now, history>>
\* Grace runs out: endJobs cancels the running jobs' context.
GraceOver(p) ==
    /\ proc[p] = "stopping" /\ proc' = [proc EXCEPT ![p] = "graceOver"]
    /\ UNCHANGED <<rowVars, row, hb, wVars, fly, pend, now, history>>

\* A finished job's result lost with its process: a rerun after it is the
\* at-least-once the docs promise, not a bug.
LosesOk(p) == \/ \E s \in Slots : pc[<<p, s>>] = "settle" /\ wRes[<<p, s>>] = "ok"
              \/ \E r \in pend[p] : r.kind = "finished"

\* Every job has returned; the heartbeat stops, the last retry fails or
\* isn't needed, and the deferred DELETE removes the process row.
Exit(p) ==
    /\ proc[p] \in {"stopping", "graceOver"} /\ \A s \in Slots : pc[<<p, s>>] = "idle"
    /\ proc' = [proc EXCEPT ![p] = "gone"] /\ row' = [row EXCEPT ![p] = FALSE]
    /\ fly' = [fly EXCEPT ![p] = {}] /\ pend' = [pend EXCEPT ![p] = {}]
    /\ okLost' = (okLost \/ LosesOk(p))
    /\ UNCHANGED <<rowVars, hb, wVars, now, starts, putBacks, reclaims, exhausted, okDone, rerunAfterOk, retried>>

\* kill -9, the OOM killer, a panic outside perform: anywhere; the rows stay.
Crash(p) ==
    /\ Crashes /\ Live(p)
    /\ proc' = [proc EXCEPT ![p] = "gone"]
    /\ pc' = [w \in W |-> IF w[1] = p THEN "idle" ELSE pc[w]]
    /\ wAt' = [w \in W |-> IF w[1] = p THEN 0 ELSE wAt[w]] /\ wAtt' = [w \in W |-> IF w[1] = p THEN 0 ELSE wAtt[w]]
    /\ wRes' = [w \in W |-> IF w[1] = p THEN "none" ELSE wRes[w]]
    /\ fly' = [fly EXCEPT ![p] = {}] /\ pend' = [pend EXCEPT ![p] = {}]
    /\ okLost' = (okLost \/ LosesOk(p))
    /\ UNCHANGED <<rowVars, row, hb, now, starts, putBacks, reclaims, exhausted, okDone, rerunAfterOk, retried>>

\* Retry: failed -> ready, attempts and crashes 0, run_at now; claimed_by and
\* claimed_at stay. Someone chose to run it again, so the history restarts.
\* Once a behavior: another would start the same history over.
RetryFailed ==
    /\ st = "failed" /\ ~retried
    /\ st' = "ready" /\ att' = 0 /\ crashes' = 0 /\ runAt' = now
    /\ starts' = 0 /\ putBacks' = 0 /\ reclaims' = 0 /\ exhausted' = 0
    /\ okDone' = FALSE /\ rerunAfterOk' = FALSE /\ okLost' = FALSE /\ retried' = TRUE
    /\ UNCHANGED <<by, at, procVars, wVars, fly, pend, now>>

\* Time passes: each beating process has beaten within HB, none is in its
\* grace, and no beating process has an orphaned claim due.
Tick ==
    /\ now < MaxTime
    /\ \A p \in Procs : Beating(p) => now + 1 <= hb[p] + HB
    /\ \A p \in Procs : proc[p] # "stopping"
    /\ ~MutNoOrphanRelease => \A p \in Procs : Beating(p) => ~Orphaned(p)
    /\ now' = now + 1
    /\ UNCHANGED <<rowVars, procVars, wVars, fly, pend, history>>

Next ==
    \/ \E p \in Procs : \/ Start(p) \/ Beat(p) \/ Release(p) \/ \E r \in pend[p] : Resettle(p, r)
                        \/ Maintain(p) \/ MaintainReclaim(p) \/ MaintainCrashFail(p)
                        \/ Shutdown(p) \/ GraceOver(p) \/ Exit(p) \/ Crash(p)
    \/ \E w \in W : \/ Claim(w) \/ ClaimCommitFails(w) \/ ClaimCommitAmbiguous(w)
                    \/ Return(w, "ok") \/ Return(w, "err")
                    \/ Settle(w) \/ SettleFails(w) \/ SettleAmbiguous(w)
    \/ RetryFailed
    \/ Tick

Spec == Init /\ [][Next]_vars

\* The processes are interchangeable.
Sym == Permutations(Procs)

\* A claim whose commit failed can run again and again without the clock moving under MutFoundBeforeCommit.
Bound == starts <= MaxAttempts + 3

\* Two workers never run the job at once.
NoConcurrentExecution == Cardinality({w \in W : pc[w] = "perform"}) <= 1
\* Runs since the last Retry: Retry.Attempts, plus shutdown put-backs and crash recoveries.
AttemptsBounded == starts <= MaxAttempts + putBacks + reclaims
\* A job whose process died MaxCrashes times isn't put back.
CrashLoopBounded == st \in {"ready", "claimed"} => crashes < MaxCrashes
\* A claimed job is being run or settled, or its process is gone (maintenance
\* frees it), or it's within one beat of the margin a heartbeat gives it.
NoStuckClaim == st = "claimed" =>
    \/ proc[by] \in {"new", "gone"}
    \/ \E s \in Slots : pc[<<by, s>>] # "idle" /\ wAt[<<by, s>>] = at
    \/ \E r \in pend[by] : r.at = at
    \/ at + HB + 1 >= now
\* A job runs again after one of its runs returned nil only when that result
\* died with its process.
NoAvoidableRerun == rerunAfterOk => okLost
\* OnExhausted runs at most once a failure, and runs once a live process's
\* failed settle is saved.
ExhaustedOnce == exhausted <= 1
FailedRunsOnExhausted ==
    (st = "failed" /\ proc[by] # "gone" /\ ~\E r \in pend[by] : r.at = at) => exhausted >= 1

\* Reachability witnesses: each is violated by its spec/Jobs.Reach*.cfg.
\* A crashed process's claim put back, and one failed on its last crash:
ReachCrashRecovery == ~(st = "ready" /\ reclaims > 0)
ReachCrashFail == ~(st = "failed" /\ crashes = MaxCrashes)
\* A kept settle that its retry saves:
ReachSettleRetried == ~\E p \in Procs : Live(p) /\ \E r \in pend[p] : Fence(p, r.at, r.kind, FALSE)
\* A claim of a beating process that no worker runs, due to be put back:
ReachOrphanReleased == ~\E p \in Procs : Beating(p) /\ Orphaned(p)
\* A claim whose commit errored but went through, not run:
ReachClaimCommitFailed == ~(st = "claimed" /\ proc[by] = "up" /\ at \notin fly[by])
====
