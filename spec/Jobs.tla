---- MODULE Jobs ----
EXTENDS Naturals

CONSTANTS Workers, Slots, MaxAttempts, MaxFence

VARIABLES
    state,
    claimedBy,
    claimedAt,
    attempts,
    nextFence,
    workerActive,
    workerFence,
    settledCount,
    slotScheduled

vars == <<state, claimedBy, claimedAt, attempts, nextFence, workerActive, workerFence, settledCount, slotScheduled>>

TypeOK ==
    /\ state \in {"ready", "claimed", "done", "failed"}
    /\ claimedBy \in Workers \cup {"none"}
    /\ claimedAt \in 0..MaxFence
    /\ attempts \in 0..MaxAttempts
    /\ nextFence \in 1..(MaxFence + 1)
    /\ workerActive \in [Workers -> BOOLEAN]
    /\ workerFence \in [Workers -> 0..MaxFence]
    /\ settledCount \in 0..2
    /\ slotScheduled \in [Slots -> 0..2]

Init ==
    /\ state = "ready"
    /\ claimedBy = "none"
    /\ claimedAt = 0
    /\ attempts = 0
    /\ nextFence = 1
    /\ workerActive = [w \in Workers |-> FALSE]
    /\ workerFence = [w \in Workers |-> 0]
    /\ settledCount = 0
    /\ slotScheduled = [s \in Slots |-> 0]

Claim(w) ==
    /\ state = "ready"
    /\ ~workerActive[w]
    /\ attempts < MaxAttempts
    /\ nextFence <= MaxFence
    /\ state' = "claimed"
    /\ claimedBy' = w
    /\ claimedAt' = nextFence
    /\ attempts' = attempts + 1
    /\ nextFence' = nextFence + 1
    /\ workerActive' = [workerActive EXCEPT ![w] = TRUE]
    /\ workerFence' = [workerFence EXCEPT ![w] = nextFence]
    /\ UNCHANGED <<settledCount, slotScheduled>>

LeaseExpire ==
    /\ state = "claimed"
    /\ attempts < MaxAttempts
    /\ state' = "ready"
    /\ claimedBy' = "none"
    /\ claimedAt' = 0
    /\ UNCHANGED <<attempts, nextFence, workerActive, workerFence, settledCount, slotScheduled>>

SettleDone(w) ==
    /\ workerActive[w]
    /\ workerActive' = [workerActive EXCEPT ![w] = FALSE]
    /\ workerFence' = [workerFence EXCEPT ![w] = 0]
    /\ IF state = "claimed" /\ claimedBy = w /\ claimedAt = workerFence[w]
       THEN /\ state' = "done"
            /\ settledCount' = settledCount + 1
            /\ UNCHANGED <<claimedBy, claimedAt>>
       ELSE UNCHANGED <<state, claimedBy, claimedAt, settledCount>>
    /\ UNCHANGED <<attempts, nextFence, slotScheduled>>

SettleFail(w) ==
    /\ workerActive[w]
    /\ workerActive' = [workerActive EXCEPT ![w] = FALSE]
    /\ workerFence' = [workerFence EXCEPT ![w] = 0]
    /\ IF state = "claimed" /\ claimedBy = w /\ claimedAt = workerFence[w]
       THEN IF attempts < MaxAttempts
            THEN /\ state' = "ready"
                 /\ claimedBy' = "none"
                 /\ claimedAt' = 0
                 /\ UNCHANGED settledCount
            ELSE /\ state' = "failed"
                 /\ settledCount' = settledCount + 1
                 /\ UNCHANGED <<claimedBy, claimedAt>>
       ELSE UNCHANGED <<state, claimedBy, claimedAt, settledCount>>
    /\ UNCHANGED <<attempts, nextFence, slotScheduled>>

RecurringTick(s) ==
    /\ slotScheduled[s] = 0
    /\ slotScheduled' = [slotScheduled EXCEPT ![s] = 1]
    /\ UNCHANGED <<state, claimedBy, claimedAt, attempts, nextFence, workerActive, workerFence, settledCount>>

Next ==
    \/ \E w \in Workers : Claim(w)
    \/ LeaseExpire
    \/ \E w \in Workers : SettleDone(w)
    \/ \E w \in Workers : SettleFail(w)
    \/ \E s \in Slots : RecurringTick(s)

Spec == Init /\ [][Next]_vars

AtMostOnceTerminalSettle == settledCount <= 1

NoDuplicateRecurringTicks == \A s \in Slots : slotScheduled[s] <= 1

AttemptsBounded == attempts <= MaxAttempts

====
