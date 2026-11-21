# Learning etcd

This weekend, I've been spending time understanding how etcd works internally. It started with a fairly simple goal: I wanted to understand what actually happens when a Kubernetes control plane stores state, elects a leader, or recovers from failures. Although I already know that etcd is a distributed key-value store with a focus on consistency backed by Raft, I still wanted to see why it is the way it is.

Here is my attempt to document that understanding.

# Layers in etcd

Well, etcd is distributed as a single binary, but a better way to view it probably, is as a collection of layers, each solving a specific problem and handing its result to the next one.

```mermaid
flowchart TD
    Client[Client Request]
    Raft[Raft Consensus]
    WAL[WAL: Write-Ahead Log, per node]
    MVCC[MVCC Layer]
    BBolt[bbolt Storage Engine]

    Client --> Raft
    Raft --> WAL
    Raft --> MVCC
    MVCC --> BBolt
```

A client request does not immediately modify a database on disk. Before anything is persisted, the cluster first needs to agree that the operation should happen. That part is taken care of by Raft. Each node in the cluster records every Raft state change in its Write-Ahead Log (WAL), ensuring crash recovery is always possible. Once consensus is reached and the log entry is safely on disk, the operation enters the MVCC layer, which represents that agreed history as versioned data. Finally, those versions are persisted by bbolt, an embedded key-value database.

# How Raft Builds a Shared History

A key-value store on a single machine is pretty straightforward: the machine receives a write, updates the value, persists to disk, and returns success.

Replicating that database across three nodes is also pretty simple if we ignore consistency and use asynchronous replication. However, etcd is designed as a highly consistent data store, so prioritising consistency introduces a coordination problem. The challenge is not the data itself, but the history. Every replica must reconstruct the exact same sequence of operations, regardless of the order in which requests initially arrive at different nodes.

Clients can send writes simultaneously and connect to any member in the cluster. Since only the leader can accept writes, followers forward these requests to the leader. Followers never decide the order of writes themselves, only the leader does. The leader establishes a single global order by appending each write to an append-only log. Every log entry records the term in which it was created, its index in the log, and the operation to execute. The leader appends the entry to its own log and sends it to followers in parallel so they can append it to their own logs. In etcd, the leader's own disk write happens concurrently with this replication rather than before it.

> A term is a logical clock for elections. Every time a node starts an election, it increments the term, e.g. if node X starts an election in term 57 and wins, it is the leader for term 57. Some terms end without a leader at all (for example after a split vote), so the term counts election attempts, not successful elections.

Followers persist the new log entry to their Write Ahead Log (WAL) using fsync (flushing it to disk) before acknowledging it. Once a majority of the cluster has the entry durably stored (the leader counts itself, so in a three-node cluster one follower ACK is enough), the leader advances the commit index, marking the entry as committed. The leader then applies the committed entry to the state machine, i.e. the MVCC layer, whose state is persisted in bbolt, the embedded key-value store. Only after the write has been committed and applied does the leader return success to the client.

The leader also informs followers that the entry is now committed. Once followers learn that an entry has been committed, they apply it to their own state machines. Because every node applies the same log entries in the same order, every replica comes to the same state.

```mermaid
sequenceDiagram
    participant Client
    participant Leader
    participant F1 as Follower 1
    participant F2 as Follower 2

    Client->>Leader: PUT request
    Leader->>Leader: Append entry to Raft log
    par Leader persists and replicates concurrently
        Leader->>Leader: Persist entry to local WAL (fsync)
    and
        Leader->>F1: Send new log entry
    and
        Leader->>F2: Send new log entry
    end

    F1->>F1: Persist entry to local WAL (fsync)
    F1-->>Leader: ACK

    Leader->>Leader: Commit entry (leader + F1 = majority)

    F2->>F2: Persist entry to local WAL (fsync)
    F2-->>Leader: ACK (not needed for commit)
    Leader->>Leader: Apply entry to MVCC state machine

    Leader->>F1: Notify that entry is committed
    Leader->>F2: Notify that entry is committed

    F1->>F1: Apply entry to MVCC state machine
    F2->>F2: Apply entry to MVCC state machine

    Leader-->>Client: Success
```

This sequence made me understand why etcd performance depends on both network and disk latency. The client's request does not complete until the leader has replicated the entry to a majority of the cluster and those nodes have durably persisted it to disk. So, every write involves at least one round trip between the leader and followers, plus an fsync on a majority of nodes.

Network partitions illustrate why this design is robust. In a three-node cluster, if one node becomes isolated from the other two, the isolated node can no longer form a majority for new log entries, so no new writes can be committed on that side. The two connected nodes can still form a quorum, elect a new leader if necessary, and continue accepting writes. When the partition heals, the isolated node discovers that it has fallen behind and receives the missing committed log entries from the current leader until it catches up. Any uncommitted entries on the isolated node that conflict with the current leader's log are discarded and replaced. While isolated, the node may also time out and try to start elections; etcd's PreVote mechanism makes it first check whether it could actually win, so it doesn't inflate its term and disrupt the healthy cluster when it rejoins. The critical property is that a leader cannot commit new writes without confirmation from a majority of the cluster, which ensures that the committed log order is consistent across the nodes.

Raft doesn't know what a Kubernetes Pod is. It doesn't understand Deployments, Services, or distributed locks. For Raft, every client request is simply another operation to be appended to the log. That scoped responsibility is what allows it to provide the guarantee that everything else in etcd depends on: the existence of a single, globally agreed sequence of operations. Everything built on top of etcd, from Kubernetes objects to distributed locks, inherits that guarantee by relying on the same replicated log.

# Inside the Write-Ahead Log

The Raft log lives in memory as an ordered list of entries, but it also needs to survive crashes. The WAL is a durable append-only file that records every Raft state change before anything is acknowledged to the client. If a node crashes and restarts, it reads its WAL to reconstruct exactly what happened before the crash. The Raft paper mostly talks about persisting three things: the log entries, the current term, and which node received the vote (votedFor). etcd's WAL implementation stores all of those, plus a few extras:

| Record Type | Contents |
|---|---|
| Entry | The Raft log entry (term, index, command) |
| HardState | Current term, votedFor, commit index |
| Snapshot | Term and index of the latest snapshot (a marker, not the snapshot data itself) |
| Metadata | Cluster ID and Node ID |
| CRC | Running checksum carried over at the start of each new WAL file |

Every record also carries its own CRC, so corruption can be detected record by record.

Storing everything in a single append-only stream means that after a restart, etcd can reconstruct almost the entire Raft state just from the WAL.

Also as mentioned earlier, these are append only, so the WAL never modifies old bytes. If Raft decides that an entry at a given index should be replaced (for example, after a new leader is elected and conflicts are resolved), the WAL doesn't edit the old entry in place. It appends a new copy, and the new version supersedes the old one during recovery. This happens because Raft allows uncommitted entries to be overwritten. Suppose a leader in term 2 appends entry 10. Before it can replicate entry 10 to a majority, it crashes. A new leader is elected in term 3 with a shorter log that only goes up to index 9. The new leader now appends its own entry 10 with term 3. The WAL on the old leader's disk (or any follower that received entry 10) will physically contain:

```
Entry(10, term=2)   ← from old leader
Entry(10, term=3)   ← from new leader
```

Both bytes exist on disk. During recovery, etcd processes records sequentially. It stores Entry(10, term=2) first, then, when Entry(10, term=3) appears, truncates the in-memory log back to index 9 and appends the new entry. Any entries that came after the old entry 10 are dropped along with it. The final log contains entry 10 from term 3. The old copy is never touched on disk but is simply ignored. The only entries that are truly permanent are those at or below the commit index recorded in the most recent HardState. Anything above it is tentative and might be replaced on the next leader change.

## Recovery

When a node restarts, it first loads the latest snapshot from its `snap/` directory, then opens the WAL at the position marked by the matching Snapshot record and reads the records that follow, verifying each record's CRC as it goes:

1. **HardState records**: Keeps only the most recent one. This tells the node its current term, which node it voted for, and the highest committed index.
2. **Entries**: Builds the log, keeping only the newest version of any conflicting (same index) entries.

The result is a reconstructed Raft log and state that is identical to what existed before the crash.

In etcd v3 the state machine itself lives in bbolt, which is already durable on disk, and bbolt records the index of the last applied entry (the consistent index). So on restart a node only re-applies entries newer than that. Snapshots matter for a different reason: they let etcd discard old log entries and purge old WAL files. Without them, a node that had processed a lot of log entries, let's say a million, would keep all of them in its log and on disk forever. With a snapshot taken at, say, index 300,000, everything before that point can be thrown away. When a follower falls far behind and the leader no longer has the old log entries, the leader sends a snapshot directly (in etcd v3, that is the bbolt database file itself). The follower applies the snapshot and then continues with normal replication from that point forward.

# MVCC (Multi-Version Concurrency Control)

Once the cluster agrees on what happened, etcd has to decide how to represent that history. If all etcd needed was to return the current value for a key, overwriting the previous value would be the simplest design.

Kubernetes asks questions that depend on change: 
- Has this object been modified since I last checked? 
- Can I resume watching from where I disconnected? 
- Is the state I'm about to update still what I originally read? 

These questions are not about values, they are about history. 

MVCC preserves that history by keeping multiple versions of each value alive. But the important detail is that revisions are global, not per key. Every successful write anywhere in the keyspace advances a single global revision counter. In the example below, the database configuration key jumps from Revision 2 to Revision 5 not because it was updated three times, but because writes to other keys happened in between. This makes sense when you connect it back to Raft, which established a total ordering of operations, and MVCC preserves that ordering by assigning each committed keyspace change the next revision number. Raft entries that don't modify keys, such as lease grants, compactions, or a transaction whose comparison fails, don't advance the revision. A transaction that writes several keys gets a single revision shared by all of its changes. The revision doesn't describe the history of a single key, it describes the history of the entire cluster.

In etcd, when you read a key, the response includes two additional fields: **mod_revision** and **version**. They sound similar but serve different purposes. Consider our database configuration key again, this time showing all the writes happening across the cluster:

```
Global Rev 1: (empty cluster, no writes yet)
Global Rev 2: PUT /config/database = mysql     → mod_revision=2, version=1
Global Rev 3: PUT /nodes/node1 = Ready         (different key)
Global Rev 4: PUT /pods/frontend = Running     (different key)
Global Rev 5: PUT /config/database = postgres  → mod_revision=5, version=2
  ... Revs 6-8: writes to other keys ...
Global Rev 9: PUT /config/database = mongodb   → mod_revision=9, version=3
```

**mod_revision** is the global revision at which this specific key was last written. After the third write, `/config/database` has mod_revision 9, because that write happened at global revision 9. A related field, **create_revision**, records the revision at which the key was created (2 here). This directly connects the per key change to a position in the cluster-wide timeline. It's what transactions use for optimistic concurrency, you read a key, note its mod_revision, then in a later transaction you compare that the key's mod_revision hasn't changed before writing.

**version** is a per-key counter that starts at 1 when the key is created and increments on each subsequent write to that same key. Unlike mod_revision, it doesn't correspond to any global position. The database key was written three times, so its version is 3. It arrived at that number across global revisions 2, 5, and 9 because other keys were modified in between, but version doesn't track the gaps, it only counts modifications to that one key. If the key is deleted, its version resets, and recreating it starts again at 1. This is useful for scenarios where you need to know if the number of changes to a key has grown, regardless of what else happened in the cluster.

If you've used MVCC databases like MySQL or PostgreSQL, the term "MVCC" probably means something different from what I just described. In SQL databases, MVCC is primarily a concurrency mechanism. It lets readers continue seeing a consistent view of the database even while other transactions are modifying the same data. If you're halfway through a query and someone updates a row, your query doesn't suddenly see the new value, it keeps seeing the version that existed when your transaction began. This is what allows readers and writers to proceed without blocking each other.

etcd's MVCC serves a fundamentally different purpose. Raft already serializes all writes into a single linearized order, so etcd doesn't need MVCC to coordinate concurrent transactions. Instead, it uses versioning to preserve that global history. Rather than exposing only the latest value for a key, etcd keeps every version until it's explicitly compacted, allowing clients to read the state of the cluster at any revision. In contrast, SQL databases preserve older versions only long enough to ensure active transactions go well, once no transaction needs them anymore, those versions disappear.

| SQL MVCC                               | etcd MVCC                                |
| -------------------------------------- | ---------------------------------------- |
| Keeps old versions temporarily     | Keeps every version until compaction |
| Snapshot by transaction start time | Snapshot by requested revision       |
| Concurrency control                | Historical versioning                |

This distinction matters because it explains why etcd can support features like watches and revision-based transactions natively. The history is not a side effect of concurrency control, instead it is the primary data structure. Clients do not just see "the current value," they can navigate the cluster's entire timeline.

Revisions are logical timestamps that represent a position in the shared history, unaffected by clock skew or machine boundaries. This lets clients read a consistent snapshot at a specific revision while writes continue in the background. Once that global revision exists, features throughout etcd start using it as a common reference point. E.g. a watch asks "tell me what happened after Revision 1500" or the transactions check "has anyone modified this key since Revision 824?".

# Persistence with bbolt

In etcd, Raft handles coordination and consensus. MVCC handles versioning and historical reads. But when it comes to actually persisting all of that data to disk, etcd relies on bbolt. It is an embedded key-value database. It has no idea what MVCC is, and it certainly doesn't know anything about Raft or distributed systems. As far as it is concerned, it is running inside a single process on a single machine, storing pages on disk and making sure committed transactions survive crashes. Everything else is built on top of it.

> bbolt is a community-maintained fork of the original BoltDB. BoltDB was created as an embedded key-value database with ACID transactions and a single-file storage model. After the original project was archived, the community forked it to create bbolt, which continues to be actively maintained.

## Copy on Write Pages

Internally, bbolt stores its data in a B+tree, but the important detail for etcd is that the tree is copy-on-write. When a write transaction updates a key, bbolt never overwrites existing pages. Instead, it allocates new pages for the modified path through the tree, leaving the old pages untouched. Any read transaction that started before the write continues reading the old pages, while new transactions see the updated tree.

## Single Writer, Multi Reader Model

bbolt allows only one read-write transaction at a time, while any number of read-only transactions can run concurrently, which fits in with the workload type of etcd. Raft already serializes every write into a single global order, so there is never a need for multiple concurrent writers. Applied Raft entries are written into a single long-running bbolt write transaction that etcd commits in batches (by default every 100ms or every 10,000 operations), while readers continue accessing older snapshots through copy-on-write pages. Batching is safe because the WAL, not bbolt, is what makes a write durable: if a node crashes before a batch is committed, it simply re-applies those entries from the WAL on restart.

## Space reclamation
Compaction removes old revisions from the MVCC history. The pages that held them, once no longer referenced by any active transaction, go onto bbolt's freelist and are reused for new writes. However, the database file itself never shrinks on its own. To return that space to the filesystem, an operator (or an external tool or cronjob) has to run defragmentation, e.g. `etcdctl defrag`, which rewrites the database into a new, compact file.

## Why not SQLite?

At first glance, SQLite looks like it would fit perfectly, in fact Kine uses it. It's also an embedded, ACID database that stores everything in a single file. So why does etcd use bbolt instead?

The answer is that etcd doesn't need a relational database or a SQL query engine. It already knows exactly how its data is organized, i.e. everything is a key-value pair. Adding SQL parsing, query planning, indexes, and the relational model would just introduce complexity that etcd never uses. More importantly, bbolt's programming model aligns naturally with etcd's architecture. It provides a simple ordered key-value store with copy-on-write transactions and a single-writer model—the same assumptions Raft already makes by serializing all writes through the leader. 

| SQLite                              | bbolt                         |
| ----------------------------------- | ----------------------------- |
| General-purpose relational database | Simple ordered KV store       |
| SQL engine                          | No query language             |
| Designed for arbitrary applications | Designed as a storage library |
| Rich features                       | Minimal primitives            |

etcd can build MVCC, watches, leases, and the rest of its functionality directly on top of that primitive without having to work around a full relational database sitting underneath it.

# Why this works for etcd

This pattern appears in other projects too. BuildKit, the container image build system, also uses bbolt. At first that seems unrelated, but the workloads share an important characteristic. BuildKit stores metadata about build cache entries, snapshots, content digests, and dependency graphs. It already knows the key, e.g. a digest, a snapshot ID, a cache identifier and just needs to look up the associated metadata transactionally. It is not running exploratory queries or analytical scans. That is the same workload etcd has: most operations already know the key and just need efficient, transactional lookups. An embedded key-value store that excels at key-based access with ACID guarantees is a natural fit for both.

```mermaid
flowchart LR
    A["Known Key (digest, revision, key)"]
    B["Lookup Metadata in bbolt"]
    C["Locate or Construct Related Data"]

    A --> B --> C
```

The fsync behavior I mentioned during Raft's commit flow is worth revisiting here because it's a practical concern when running etcd. The fsync that sits on a client's critical path is the WAL fsync, not bbolt's. bbolt commits are batched in the background, while every write must be fsynced to the WAL on a majority of nodes before it can be committed. So the write latency a client experiences is roughly: one network round trip to the leader + replication to followers in parallel with the leader's own WAL fsync (network + WAL fsync on each) + applying the entry + response back. The slowest step tends to be the WAL fsync, which is why etcd documentation strongly recommends SSDs with low fsync latency and warns against shared or networked filesystems like NFS.

> Have you ever run sqlite with NFS? xd

# Guarantees Provided by etcd

All of these layers, i.e. Raft, WAL, MVCC and bbolt work together to deliver a set of guarantees that clients can rely on. Understanding these guarantees clarifies why certain design decisions were made and what trade-offs they involve.

## KV operations are strictly serializable

For KV operations (Put, Range, Delete, Txn), etcd provides **strict serializability**. This is the strongest isolation guarantee for a distributed database. It means:
- **Atomicity**: Each operation either completes entirely or not at all.
- **Total order**: All operations occur in a single global order that is consistent with real time. If operation A completes before operation B begins, A appears before B in the commit order.
- **Durability**: Once an operation completes, its effects are permanent. A read will never return data that hasn't been made durable.

Strict serializability is achieved through Raft: every write goes through consensus, which produces the total order. The revision assigned to each write operation is the observable manifestation of that order. Changes that share the same revision were applied atomically as part of the same transaction.

## Linearizability

From a client's perspective, strict serializability implies **linearizability**: each operation appears to take effect instantaneously at some point between when the client sent it and when the client received the response.

```mermaid
sequenceDiagram
    participant Client
    participant etcd as etcd Cluster

    Note over Client,etcd: t1: Client sends PUT(x=1)
    Client->>etcd: PUT x=1
    Note over Client,etcd: t2: PUT committed
    etcd-->>Client: OK

    Note over Client,etcd: t3: Client sends GET(x)
    Client->>etcd: GET x
    Note over Client,etcd: t4: GET returns
    etcd-->>Client: x=1 (at least as recent as t2)
```

If a write completes at time t2, any read that starts after t2 must return the result of that write (or a later one). This is a powerful guarantee because it means clients doing linearizable reads don't need to worry about stale data.

Linearizability comes with a cost. Writes go through the full Raft log. Linearizable reads don't create log entries, but they use the ReadIndex mechanism: the serving node gets the current commit index from the leader, the leader confirms it is still leader by exchanging heartbeats with a majority of the cluster, and the node waits until it has applied up to that index before answering. That quorum round trip adds latency. For read-heavy workloads that can tolerate slightly stale data, etcd offers a **serializable** read mode that bypasses consensus and reads directly from the local node's state machine. This is faster but may return data that doesn't reflect the latest committed writes.

## Watch guarantees

Watches subscribe to changes over a key range. They make the following guarantees:
- **Ordered**: Events are delivered in revision order. You will never see a later event before an earlier one.
- **Unique**: A watcher never sees the same event twice.
- **Reliable**: No events are dropped within the available history window. If events A, B, and C occur in that order, and you receive A and C, you are guaranteed to also receive B. The final sequence you observe will always be A, B, C (assuming those revisions haven't been compacted).
- **Atomic**: Events from a single transaction (which may touch multiple keys) are delivered together in one response. You never see partial results.
- **Resumable**: If a watch connection breaks, you can create a new watch starting from the revision after the last one you received. As long as that revision hasn't been compacted away, you won't miss anything.
- **Bookmarkable**: Clients can request progress notifications (periodically or on demand), which guarantee that all events up to a certain revision have been delivered.

Watches are not linearizable. The revision field on each event tells you where it falls in the global order, so you can reconcile watch events with other operations yourself.

## Lease behavior

Leases follow the same durability rules: a lease is durable once its Grant operation commits. However, lease expiration is driven by time, not by Raft consensus, hence its behavior is a bit different. Only the leader tracks lease expiry, using its own monotonic clock. When a lease expires, the leader proposes the revocation through Raft, and every member then deletes the attached keys as part of the normal apply flow. Followers never expire leases on their own. Because expiry is tied to the leader, a leader change affects it: the new leader extends the remaining TTL of existing leases so they don't expire spuriously during the transition, which means a lease can outlive its nominal TTL. etcd's lease checkpointing periodically persists remaining TTLs through Raft to limit how much they reset on a leader change.

# Leases

The core problem is that in a distributed system, processes claim temporary ownership of things, e.g. a service registering itself, a controller becoming leader, a worker claiming a task. The difficult question is what happens when the process disappears. Machines lose power, containers are killed, networks fail before graceful shutdown completes. If ownership depended on explicit cleanup, stale information would accumulate quickly.
 
etcd solves this by making temporary ownership a first-class concept. A client requests a lease with a configurable TTL, then attaches keys to it. As long as the client periodically renews the lease before the TTL expires, the attached keys remain valid. If the client stops sending keepalives, maybe it was part of crash, partition, termination, the lease expires and all attached keys are deleted automatically.

```mermaid
sequenceDiagram
    participant Client
    participant etcd

    Client->>etcd: Grant Lease (TTL = 30s)
    etcd-->>Client: Lease ID
    Client->>etcd: PUT /service/myapp (attach Lease)

    loop Every 10s
        Client->>etcd: KeepAlive(Lease ID)
        etcd-->>Client: TTL refreshed
    end

    Note over Client: Crash

    Note over etcd: TTL expires
    etcd->>etcd: Expire lease, delete attached keys
```

This shifts responsibility away from clients remembering to clean up after themselves. In a system where assuming graceful shutdown is optimistic, that's a significant improvement in robustness. The same idea appears far beyond etcd. DHCP uses leases for IP assignment. Cloud platforms use similar mechanisms for resource coordination. The pattern is always the same: ownership should be temporary unless someone actively proves it still exists.

# Locks, Leader Election, and Composition

Distributed locks are not built alongside leases, rather they're built from leases. When a client acquires a lock, it creates a key under a well-known prefix and attaches that key to a lease. The lock's existence is tied to the lease: if the client crashes, the lease expires, and the lock disappears without explicit cleanup.

But leases only solve the lifecycle problem. They don't determine who holds the lock when multiple clients compete simultaneously. That's where revisions come in.

Every committed operation has a globally agreed revision. When Client A creates its lock key, that key gets create_revision 120. Client B's attempt gets Revision 121. Every cluster member agrees that 120 happened before 121 because Raft and MVCC already established that ordering. The client holding the earliest creation revision owns the lock. Everyone else waits.

```mermaid
flowchart LR
    R120["Revision 120<br/>Client A (owns lock)"]
    R121["Revision 121<br/>Client B (waiting)"]
    R122["Revision 122<br/>Client C (waiting)"]

    R120 --> R121 --> R122
```

Each waiting client watches only the key directly ahead of it in this order, rather than the lock holder, so when a lock is released only the next client in line wakes up instead of the whole group.

Leader election is built the same way. Each candidate creates a key under an election prefix, attached to its lease. The candidate with the lowest creation revision is the leader, and the rest wait in line. If the leader crashes, its lease expires, its key is deleted, and the next candidate in revision order takes over. This is the same building-block pattern: leases handle lifecycle, revisions handle ordering.

# Still Learning

It looks like this is the end of my context window for now haha. But to be honest, I think I still have plenty of gaps in my understanding, and I expect it to improve as I spend more time with playing around with etcd, even though I have experience running Kubernetes clusters in production and I have tuned timeouts for controllers before for it to not be stuck in restart loop due to network latencies, but direct interaction with etcd gives working with Kubernetes a different perspective. Right now I'm working with simulating failure scenarios in a small 3 node cluster at home, killing leaders, introducing latency to network paths between nodes, watching what happens to watches during partitions, and seeing how systems behave.