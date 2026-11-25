---- MODULE Ticks ----
\* One recurring job (Every Period) as jobs/recurring.go and maintenance have
\* it: Define sets the first due time; tick reads its clock once, then runs
\* its transaction (the gantry_job_ticks row, ON CONFLICT DO NOTHING, and the
\* job when the row is new), skipping a due time older than Keep;
\* maintenance deletes tick rows older than Keep + Margin.
\*
\* Stall bounds how long a tick's transaction waits after its clock read (a
\* lock, a frozen process); Prompt says the tick loop reads a due time before
\* the clock moves on. With Prompt FALSE a process may sit on a due time as
\* long as it likes, as a process frozen for a day does.
\*
\* Each Mut* constant puts one thing back the way it was, and
\* spec/Ticks.<Mut>.cfg checks that NoDuplicateTick then fails:
\*   MutNoMargin  B5: maintenance deletes ticks at Keep, the tick's own
\*                cutoff, so a tick that read its clock just inside Keep
\*                inserts again a run another process's maintenance deleted
\*   MutNoDedupe  the job is inserted without the tick row's check
\* A restart across a due time skips it, by design: nothing here asks for a
\* catch-up run.
EXTENDS Naturals

CONSTANTS Procs, Period, Keep, Margin, Stall, MaxTime, Prompt, MutNoMargin, MutNoDedupe

Next(t) == (t \div Period) * Period + Period
Slots == {s \in 1..MaxTime : s % Period = 0}

VARIABLES now, proc, due, tpc, snap, pend, ticks, inserted, startAt, stopAt

vars == <<now, proc, due, tpc, snap, pend, ticks, inserted, startAt, stopAt>>

Init == /\ now = 1 /\ proc = [p \in Procs |-> "new"] /\ due = [p \in Procs |-> 0]
        /\ tpc = [p \in Procs |-> "idle"] /\ snap = [p \in Procs |-> 0] /\ pend = [p \in Procs |-> 0]
        /\ ticks = {} /\ inserted = [s \in Slots |-> 0]
        /\ startAt = [p \in Procs |-> 0] /\ stopAt = [p \in Procs |-> 0]

Up == {p \in Procs : proc[p] = "up"}

\* Define and recur: due = next(now).
Start(p) == /\ proc[p] = "new"
            /\ proc' = [proc EXCEPT ![p] = "up"] /\ due' = [due EXCEPT ![p] = Next(now)]
            /\ startAt' = [startAt EXCEPT ![p] = now]
            /\ UNCHANGED <<now, tpc, snap, pend, ticks, inserted, stopAt>>

\* tick: its clock read once; the due time collected.
TickRead(p) == /\ p \in Up /\ tpc[p] = "idle" /\ now >= due[p]
               /\ tpc' = [tpc EXCEPT ![p] = "tx"] /\ snap' = [snap EXCEPT ![p] = now] /\ pend' = [pend EXCEPT ![p] = due[p]]
               /\ UNCHANGED <<now, proc, due, ticks, inserted, startAt, stopAt>>

\* The transaction, with the clock tick read; then due = next(that clock).
TickTx(p) == /\ p \in Up /\ tpc[p] = "tx"
             /\ LET stale == pend[p] + Keep < snap[p]
                    fresh == MutNoDedupe \/ pend[p] \notin ticks
                IN IF ~stale /\ fresh
                   THEN /\ ticks' = ticks \cup {pend[p]}
                        /\ inserted' = [inserted EXCEPT ![pend[p]] = @ + 1]
                   ELSE UNCHANGED <<ticks, inserted>>
             /\ due' = [due EXCEPT ![p] = Next(snap[p])]
             /\ tpc' = [tpc EXCEPT ![p] = "idle"]
             /\ UNCHANGED <<now, proc, snap, pend, startAt, stopAt>>

\* The transaction errors: due stays, for the next loop.
TickFails(p) == /\ p \in Up /\ tpc[p] = "tx" /\ tpc' = [tpc EXCEPT ![p] = "idle"]
                /\ UNCHANGED <<now, proc, due, snap, pend, ticks, inserted, startAt, stopAt>>

\* maintain: DELETE FROM gantry_job_ticks WHERE run_at < now - Keep - tickMargin.
Kept(t) == t + Keep + (IF MutNoMargin THEN 0 ELSE Margin) >= now
Maintain(p) == /\ p \in Up /\ \E t \in ticks : ~Kept(t)
               /\ ticks' = {t \in ticks : Kept(t)}
               /\ UNCHANGED <<now, proc, due, tpc, snap, pend, inserted, startAt, stopAt>>

Stop(p) == /\ p \in Up /\ proc' = [proc EXCEPT ![p] = "gone"] /\ tpc' = [tpc EXCEPT ![p] = "idle"]
           /\ stopAt' = [stopAt EXCEPT ![p] = now]
           /\ UNCHANGED <<now, due, snap, pend, ticks, inserted, startAt>>

\* Time passes: no tick's transaction has waited Stall since its clock read,
\* and with Prompt no up process has a due time it hasn't read.
Tick == /\ now < MaxTime
        /\ \A p \in Up : tpc[p] = "tx" => now + 1 <= snap[p] + Stall
        /\ Prompt => \A p \in Up : tpc[p] = "idle" => now < due[p]
        /\ now' = now + 1
        /\ UNCHANGED <<proc, due, tpc, snap, pend, ticks, inserted, startAt, stopAt>>

Nxt == \/ \E p \in Procs : Start(p) \/ TickRead(p) \/ TickTx(p) \/ TickFails(p) \/ Maintain(p) \/ Stop(p)
       \/ Tick
Spec == Init /\ [][Nxt]_vars

TypeOK == /\ now \in 1..MaxTime /\ ticks \subseteq Slots /\ inserted \in [Slots -> 0..3]

\* One job per due time, ever (the history, not the live rows).
NoDuplicateTick == \A s \in Slots : inserted[s] <= 1
\* A due time a process was up across (started before it, up after it) gets
\* its job, or is about to. It holds only with Prompt and no Stall: a process
\* that stops between its clock read and its transaction skips that run.
Pending(s) == \E p \in Up : (tpc[p] = "idle" /\ due[p] <= s) \/ (tpc[p] = "tx" /\ pend[p] <= s)
UpAcross(s) == \E p \in Procs : proc[p] # "new" /\ startAt[p] < s /\ (proc[p] = "up" \/ stopAt[p] > s)
NoSkippedTickWhileUp == \A s \in Slots : (UpAcross(s) /\ s < now) => (inserted[s] >= 1 \/ Pending(s))

\* Reachability witness, violated by spec/Ticks.ReachDupWindow.cfg: a tick's
\* due time inside Keep by its clock and outside it by the clock now.
ReachDupWindow == ~(\E p \in Up : tpc[p] = "tx" /\ pend[p] + Keep >= snap[p] /\ pend[p] + Keep < now)
====
