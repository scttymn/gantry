---- MODULE PageCache ----
EXTENDS Naturals

CONSTANTS SignedInUsers, MaxVersion

Visitors == {"public"} \cup SignedInUsers

VARIABLES
    dbVersion,
    cachePresent,
    cacheVersion,
    cacheOwner,
    servedOk,
    servedVersion,
    servedOwner,
    servedReqVersion

vars == <<dbVersion, cachePresent, cacheVersion, cacheOwner, servedOk, servedVersion, servedOwner, servedReqVersion>>

TypeOK ==
    /\ dbVersion \in 1..MaxVersion
    /\ cachePresent \in BOOLEAN
    /\ cacheVersion \in 0..MaxVersion
    /\ cacheOwner \in Visitors
    /\ servedOk \in [Visitors -> BOOLEAN]
    /\ servedVersion \in [Visitors -> 0..MaxVersion]
    /\ servedOwner \in [Visitors -> Visitors]
    /\ servedReqVersion \in [Visitors -> 0..MaxVersion]

Init ==
    /\ dbVersion = 1
    /\ cachePresent = FALSE
    /\ cacheVersion = 0
    /\ cacheOwner = "public"
    /\ servedOk = [v \in Visitors |-> FALSE]
    /\ servedVersion = [v \in Visitors |-> 0]
    /\ servedOwner = [v \in Visitors |-> "public"]
    /\ servedReqVersion = [v \in Visitors |-> 0]

WriteDB ==
    /\ dbVersion < MaxVersion
    /\ dbVersion' = dbVersion + 1
    /\ UNCHANGED <<cachePresent, cacheVersion, cacheOwner, servedOk, servedVersion, servedOwner, servedReqVersion>>

PublicCacheHit ==
    /\ cachePresent
    /\ cacheVersion = dbVersion
    /\ servedOk' = [servedOk EXCEPT !["public"] = TRUE]
    /\ servedVersion' = [servedVersion EXCEPT !["public"] = cacheVersion]
    /\ servedOwner' = [servedOwner EXCEPT !["public"] = cacheOwner]
    /\ servedReqVersion' = [servedReqVersion EXCEPT !["public"] = dbVersion]
    /\ UNCHANGED <<dbVersion, cachePresent, cacheVersion, cacheOwner>>

PublicCacheMiss(setsCookieOrPrivate) ==
    /\ (~cachePresent \/ cacheVersion # dbVersion)
    /\ servedOk' = [servedOk EXCEPT !["public"] = TRUE]
    /\ servedVersion' = [servedVersion EXCEPT !["public"] = dbVersion]
    /\ servedOwner' = [servedOwner EXCEPT !["public"] = "public"]
    /\ servedReqVersion' = [servedReqVersion EXCEPT !["public"] = dbVersion]
    /\ IF setsCookieOrPrivate
       THEN UNCHANGED <<cachePresent, cacheVersion, cacheOwner>>
       ELSE /\ cachePresent' = TRUE
            /\ cacheVersion' = dbVersion
            /\ cacheOwner' = "public"
    /\ UNCHANGED dbVersion

SignedInRequest(u) ==
    /\ u \in SignedInUsers
    /\ servedOk' = [servedOk EXCEPT ![u] = TRUE]
    /\ servedVersion' = [servedVersion EXCEPT ![u] = dbVersion]
    /\ servedOwner' = [servedOwner EXCEPT ![u] = u]
    /\ servedReqVersion' = [servedReqVersion EXCEPT ![u] = dbVersion]
    /\ UNCHANGED <<dbVersion, cachePresent, cacheVersion, cacheOwner>>

Evict ==
    /\ cachePresent
    /\ cachePresent' = FALSE
    /\ UNCHANGED <<dbVersion, cacheVersion, cacheOwner, servedOk, servedVersion, servedOwner, servedReqVersion>>

Next ==
    \/ WriteDB
    \/ PublicCacheHit
    \/ PublicCacheMiss(FALSE)
    \/ PublicCacheMiss(TRUE)
    \/ \E u \in SignedInUsers : SignedInRequest(u)
    \/ Evict

Spec == Init /\ [][Next]_vars

NoPersonalizedEntryStored == cachePresent => cacheOwner = "public"

NoCrossVisitorLeak ==
    \A v \in Visitors :
        servedOk[v] => (servedOwner[v] = "public" \/ servedOwner[v] = v)

NoStaleVersionServed ==
    \A v \in Visitors :
        servedOk[v] => servedVersion[v] = servedReqVersion[v]

====
