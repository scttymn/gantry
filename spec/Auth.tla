---- MODULE Auth ----
EXTENDS Naturals

CONSTANTS Users, MaxResetInFlight

VARIABLES
    exists,
    verified,
    socialLinked,
    setupCompleted,
    setupCompletions,
    activeSessions,
    revokedSessions,
    inFlightResets

vars == <<exists, verified, socialLinked, setupCompleted, setupCompletions, activeSessions, revokedSessions, inFlightResets>>

TypeOK ==
    /\ exists \in [Users -> BOOLEAN]
    /\ verified \in [Users -> BOOLEAN]
    /\ socialLinked \in [Users -> BOOLEAN]
    /\ setupCompleted \in BOOLEAN
    /\ setupCompletions \in 0..2
    /\ activeSessions \subseteq Users
    /\ revokedSessions \subseteq Users
    /\ inFlightResets \in 0..(MaxResetInFlight + 1)

Init ==
    /\ exists = [u \in Users |-> FALSE]
    /\ verified = [u \in Users |-> FALSE]
    /\ socialLinked = [u \in Users |-> FALSE]
    /\ setupCompleted = FALSE
    /\ setupCompletions = 0
    /\ activeSessions = {}
    /\ revokedSessions = {}
    /\ inFlightResets = 0

CompleteSetup(u) ==
    /\ ~setupCompleted
    /\ exists' = [exists EXCEPT ![u] = TRUE]
    /\ verified' = [verified EXCEPT ![u] = TRUE]
    /\ setupCompleted' = TRUE
    /\ setupCompletions' = setupCompletions + 1
    /\ UNCHANGED <<socialLinked, activeSessions, revokedSessions, inFlightResets>>

CreateUnverifiedUser(u) ==
    /\ ~exists[u]
    /\ exists' = [exists EXCEPT ![u] = TRUE]
    /\ verified' = [verified EXCEPT ![u] = FALSE]
    /\ socialLinked' = [socialLinked EXCEPT ![u] = FALSE]
    /\ UNCHANGED <<setupCompleted, setupCompletions, activeSessions, revokedSessions, inFlightResets>>

VerifyEmail(u) ==
    /\ exists[u]
    /\ ~verified[u]
    /\ verified' = [verified EXCEPT ![u] = TRUE]
    /\ UNCHANGED <<exists, socialLinked, setupCompleted, setupCompletions, activeSessions, revokedSessions, inFlightResets>>

DeleteAllUsers ==
    /\ \E u \in Users : exists[u]
    /\ exists' = [u \in Users |-> FALSE]
    /\ verified' = [u \in Users |-> FALSE]
    /\ socialLinked' = [u \in Users |-> FALSE]
    /\ revokedSessions' = revokedSessions \cup activeSessions
    /\ activeSessions' = {}
    /\ UNCHANGED <<setupCompleted, setupCompletions, inFlightResets>>

SocialLoginLink(u) ==
    /\ IF exists[u]
       THEN /\ verified[u]
            /\ socialLinked' = [socialLinked EXCEPT ![u] = TRUE]
            /\ UNCHANGED <<exists, verified>>
       ELSE /\ exists' = [exists EXCEPT ![u] = TRUE]
            /\ verified' = [verified EXCEPT ![u] = TRUE]
            /\ socialLinked' = [socialLinked EXCEPT ![u] = TRUE]
    /\ UNCHANGED <<setupCompleted, setupCompletions, activeSessions, revokedSessions, inFlightResets>>

StartSession(u) ==
    /\ exists[u]
    /\ activeSessions' = activeSessions \cup {u}
    /\ revokedSessions' = revokedSessions \ {u}
    /\ UNCHANGED <<exists, verified, socialLinked, setupCompleted, setupCompletions, inFlightResets>>

Logout(u) ==
    /\ u \in activeSessions
    /\ activeSessions' = activeSessions \ {u}
    /\ revokedSessions' = revokedSessions \cup {u}
    /\ UNCHANGED <<exists, verified, socialLinked, setupCompleted, setupCompletions, inFlightResets>>

BeginResetSend ==
    /\ inFlightResets < MaxResetInFlight
    /\ inFlightResets' = inFlightResets + 1
    /\ UNCHANGED <<exists, verified, socialLinked, setupCompleted, setupCompletions, activeSessions, revokedSessions>>

FinishResetSend ==
    /\ inFlightResets > 0
    /\ inFlightResets' = inFlightResets - 1
    /\ UNCHANGED <<exists, verified, socialLinked, setupCompleted, setupCompletions, activeSessions, revokedSessions>>

Next ==
    \/ \E u \in Users : CompleteSetup(u)
    \/ \E u \in Users : CreateUnverifiedUser(u)
    \/ \E u \in Users : VerifyEmail(u)
    \/ DeleteAllUsers
    \/ \E u \in Users : SocialLoginLink(u)
    \/ \E u \in Users : StartSession(u)
    \/ \E u \in Users : Logout(u)
    \/ BeginResetSend
    \/ FinishResetSend

Spec == Init /\ [][Next]_vars

SingleUseSetupWizard == setupCompletions <= 1

NoUnverifiedAccountTakeover == \A u \in Users : socialLinked[u] => verified[u]

ResetSendsBounded == inFlightResets <= MaxResetInFlight

NoRevokedSessionActive == activeSessions \cap revokedSessions = {}

====
