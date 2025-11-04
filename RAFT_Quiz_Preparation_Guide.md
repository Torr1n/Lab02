# RAFT Implementation Deep Dive - Quiz Preparation Guide

**Course:** CPSC 416 - Distributed Systems
**Lab:** Lab02 - RAFT Consensus Protocol
**Implementation:** Lab02/src/raft1/raft.go (~1100 lines)
**Status:** All phases complete (3A, 3B, 3C, 3D) - All 27 tests passing

---

## Document Purpose

This guide prepares you for quiz questions that present **situations or modifications** and ask you to **reason through issues and resolution**. Unlike traditional study guides that focus on memorization, this document emphasizes:

- **WHY** decisions were made (not just WHAT the code does)
- **Reasoning patterns** for distributed systems problems
- **Critical bugs** discovered during implementation (prime quiz material!)
- **"What if?" scenarios** that test deep understanding

**Quiz Format Pattern:**
```
Scenario: [Describe modified RAFT implementation or failure scenario]
Question: What will go wrong? Why? How would you fix it?
Expected: Step-by-step reasoning using Figure 2 principles
```

---

## How to Use This Guide

### Study Approach
1. **Don't memorize** - Understand the reasoning behind each decision
2. **Practice active recall** - Cover the answer, try to reason through yourself
3. **Connect to Figure 2** - Every answer should reference Figure 2 rules
4. **Use spaced repetition** - Review over 3 days (see execution timeline)

### Document Structure
- **Part 1 (90 min):** Critical Bug Analysis - THE MOST IMPORTANT SECTION
- **Part 2 (60 min):** Figure 2 Deep Dive with exact code mappings
- **Part 3 (45 min):** Index Translation Mastery (snapshot arithmetic)
- **Part 4 (60 min):** Concurrency & Race Reasoning
- **Part 5 (90 min):** Quiz-Style Reasoning Exercises
- **Part 6 (30 min):** Test Coverage Map
- **Part 7 (Reference):** Quick Reference Cheat Sheet

**Total active study time:** ~6-8 hours over 3 days

---

# PART 1: Critical Bug Analysis (THE MOST IMPORTANT)

## Why This Section Matters

During your implementation, you discovered **3 critical bugs** that most students miss on first attempt. These bugs represent exactly the type of reasoning quizzes test:

1. Understanding **safety properties** (e.g., one vote per term)
2. Understanding **liveness properties** (e.g., election stability)
3. Understanding **API contracts** (e.g., index semantics)

Each bug follows the quiz pattern: "What breaks? Why? How to fix?"

---

## Bug 1: Double-Voting Vulnerability 🔴 CRITICAL SAFETY VIOLATION

### The Bug Discovery

**Location:** `AppendEntries` RPC handler, line 387 in raft.go

**Original (Buggy) Code:**
```go
// WRONG: Resets votedFor even on SAME-term heartbeats
if args.Term >= rf.currentTerm {
    rf.currentTerm = args.Term
    rf.votedFor = -1  // ❌ BUG: Resets on equal term!
    rf.state = Follower
    rf.persist()
}
```

**Fixed Code:**
```go
// CORRECT: Only reset votedFor on HIGHER terms
if args.Term > rf.currentTerm {  // Changed >= to >
    rf.currentTerm = args.Term
    rf.votedFor = -1  // ✅ Only reset when moving to new term
    rf.state = Follower
    rf.persist()
}
```

### What Breaks? (Step-by-Step Scenario)

**Initial State:**
- Server S is in term 5, votedFor = -1 (no vote yet)
- Candidate A sends RequestVote for term 5
- S grants vote: votedFor = 2 (candidate A's ID)

**Attack Sequence:**
1. **T=0ms:** Server S votes for Candidate A (votedFor = 2, currentTerm = 5)
2. **T=10ms:** Candidate B (who became leader) sends AppendEntries heartbeat with term 5
3. **T=10ms (buggy code):** S processes AppendEntries
   - Condition: `args.Term (5) >= rf.currentTerm (5)` → TRUE
   - Action: Sets votedFor = -1 ❌
4. **T=20ms:** Candidate C sends RequestVote for term 5
5. **T=20ms:** S grants vote to C (votedFor = 3) ❌
6. **Result:** Server S voted TWICE in term 5 (for A and C)

**Consequence:** Two leaders could be elected in same term → **SPLIT BRAIN** → safety violation

### Why This Violates Figure 2

**Figure 2 Rule (Persistent State):**
> "votedFor: candidateId that received vote in current term (or -1 if none)"

**Figure 2 Rule (All Servers):**
> "If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower (§5.1)"

**Key insight:** Figure 2 says "T > currentTerm" NOT "T >= currentTerm"

The paper is explicit: votedFor should only be reset when **entering a NEW term**, not when receiving RPCs from the SAME term.

### Reasoning Pattern (For Quiz)

**Question Template:** "Server has voted in term T. It receives AppendEntries with term T. Should votedFor be reset?"

**Reasoning Steps:**
1. **Check Figure 2 rule:** "T > currentTerm" (strict inequality)
2. **Check persistence invariant:** votedFor must persist for entire term
3. **Check safety property:** At most one vote per term
4. **Conclusion:** Only reset votedFor on term INCREASE (T > currentTerm)

**Answer:** No. Resetting votedFor on equal-term RPC violates "one vote per term" rule.

### Code References
- **Bug location:** Line 387 (AppendEntries handler)
- **Correct check:** Line 387 now uses `args.Term > rf.currentTerm`
- **Persistence:** Line 391 persists state AFTER term update
- **RequestVote grants vote:** Line 299-314 checks votedFor

### Quiz Exercise 1.1

**Scenario:** You modify the AppendEntries handler:
```go
if args.Term >= rf.currentTerm {
    rf.currentTerm = args.Term
    rf.votedFor = -1
    rf.state = Follower
}
```

**Questions:**
1. Three servers (S1, S2, S3) are in term 5. S1 votes for S2. S3 becomes leader and sends heartbeat (term 5) to S1. S1 then receives RequestVote from S2 (term 5). What happens?
2. Could two leaders be elected in term 5? Draw the sequence diagram.
3. Which Figure 2 rule is violated?
4. After S1 crashes and restarts, would it remember voting for S2?

**Answers:**
1. S1 processes S3's heartbeat → votedFor reset to -1 → S1 grants vote to S2 again → S1 voted twice
2. Yes! If S2 collects majority including S1's second vote → two leaders
3. "At most one vote per term" (implicit in votedFor persistence)
4. Yes if persist() was called before crash (votedFor is persistent state)

---

## Bug 2: Unstable Election Timeout 🟡 LIVENESS ISSUE

### The Bug Discovery

**Location:** `ticker()` goroutine and `resetElectionTimer()` helper, lines 665-668, 1007

**Original (Buggy) Code:**
```go
// In ticker() loop - WRONG: Re-samples timeout every iteration
func (rf *Raft) ticker() {
    for !rf.killed() {
        time.Sleep(50 * time.Millisecond)

        rf.mu.Lock()
        timeSinceHeartbeat := time.Since(rf.lastHeartbeat)

        // ❌ BUG: Samples NEW timeout every loop iteration
        electionTimeout := time.Duration(150+rand.Intn(150)) * time.Millisecond

        if timeSinceHeartbeat > electionTimeout {
            // Start election
        }
        rf.mu.Unlock()
    }
}
```

**Fixed Code:**
```go
// Store stable timeout in Raft struct (line 75)
type Raft struct {
    // ...
    electionTimeout time.Duration  // ✅ Stable until reset
}

// Sample timeout ONCE per reset (line 665)
func (rf *Raft) resetElectionTimer() {
    rf.lastHeartbeat = time.Now()
    rf.electionTimeout = time.Duration(150+rand.Intn(150)) * time.Millisecond
}

// In ticker() - use stored timeout (line 1007)
func (rf *Raft) ticker() {
    for !rf.killed() {
        time.Sleep(50 * time.Millisecond)

        rf.mu.Lock()
        timeSinceHeartbeat := time.Since(rf.lastHeartbeat)

        // ✅ FIXED: Use stable stored timeout
        if timeSinceHeartbeat > rf.electionTimeout {
            // Start election
        }
        rf.mu.Unlock()
    }
}
```

### What Breaks? (Step-by-Step Scenario)

**Scenario 1: Premature Election (False Positive)**
- **T=0:** Follower receives heartbeat, lastHeartbeat = now
- **T=50ms:** Ticker iteration 1
  - timeSinceHeartbeat = 50ms
  - electionTimeout = 280ms (randomly sampled) ← HIGH value
  - 50ms < 280ms → No election
- **T=100ms:** Ticker iteration 2
  - timeSinceHeartbeat = 100ms
  - electionTimeout = 160ms (randomly sampled) ← LOW value ❌
  - 100ms < 160ms → Still okay
- **T=150ms:** Ticker iteration 3
  - timeSinceHeartbeat = 150ms
  - electionTimeout = 140ms (randomly sampled) ← EVEN LOWER ❌
  - **150ms > 140ms → ELECTION TRIGGERED** even though leader is alive!

**Scenario 2: Delayed Election (False Negative)**
- Leader crashes at T=0
- Follower should elect new leader after ~200ms
- But electionTimeout keeps sampling high values (280ms, 290ms, 270ms)
- Election delayed beyond 5-second requirement → test fails

### Why This Violates RAFT Principles

**RAFT Paper Section 5.2:**
> "Election timeouts are chosen randomly from a fixed interval (e.g., 150–300ms)"

**Key word:** "chosen" (singular) - timeout is sampled ONCE per election cycle, not continuously.

**Purpose of Randomization:**
- **Primary:** Break split votes (different servers timeout at different times)
- **NOT:** Create moving target within single election cycle

**Stable timeout ensures:**
1. Follower won't start election if it recently heard from leader
2. Election timing is predictable (within chosen timeout window)
3. Randomization still works (different servers have different stored values)

### Reasoning Pattern (For Quiz)

**Question Template:** "When should election timeout be randomized? Every check or once per reset?"

**Reasoning Steps:**
1. **Identify purpose:** Why randomize? (Break split votes)
2. **Identify requirement:** Follower should wait for timeout AFTER last heartbeat
3. **Check stability:** Should timeout change while waiting? No—creates moving target
4. **Conclusion:** Sample once when reset, use stable value until next reset

**Answer:** Once per reset. Continuous re-sampling creates unpredictable election timing.

### Code References
- **Stored timeout:** Line 75 (Raft struct field)
- **Reset function:** Lines 665-668 (resetElectionTimer)
- **Usage in ticker:** Line 1007 (compares against stored value)
- **Reset triggers:** Line 400 (AppendEntries), Line 314 (RequestVote grant)

### Quiz Exercise 1.2

**Scenario:** Your ticker() re-samples timeout every iteration:
```go
for !rf.killed() {
    time.Sleep(50 * time.Millisecond)
    timeout := time.Duration(150+rand.Intn(150)) * time.Millisecond
    if time.Since(lastHeartbeat) > timeout {
        startElection()
    }
}
```

**Questions:**
1. Leader sends heartbeat every 100ms. What's probability follower starts unnecessary election between heartbeats?
2. Three servers have this bug. Leader crashes. What happens to election timing?
3. Why does this NOT break split-vote prevention?
4. Would tests pass? Which test would likely fail first?

**Answers:**
1. Depends on random draws. If timeout drops to 150ms and elapsed=100ms, no issue. But if timeout drops below elapsed time, false election.
2. Elections might be delayed (if timeouts keep sampling high) OR premature (if they sample low). Unpredictable.
3. Split-vote prevention still works because servers have different random number sequences (different seeds).
4. Probably TestManyElections3A (relies on stable election timing) or timeout assertions in test framework.

---

## Bug 3: Start() Index Return Error 🔵 API CONTRACT VIOLATION

### The Bug Discovery

**Location:** `Start()` function, lines 619-640 in raft.go

**Context:** After implementing snapshots (Phase 3D), log no longer starts at index 0. The log array represents a "window" into the logical log space.

**Hypothetical Buggy Code:**
```go
func (rf *Raft) Start(command interface{}) (int, int, bool) {
    rf.mu.Lock()
    defer rf.mu.Unlock()

    if rf.state != Leader {
        return -1, -1, false
    }

    term := rf.currentTerm
    entry := LogEntry{Term: term, Command: command}
    rf.log = append(rf.log, entry)

    // ❌ BUG: Returns slice length, not logical index
    index := len(rf.log)

    rf.persist()
    return index, term, true  // WRONG INDEX!
}
```

**Fixed Code:**
```go
func (rf *Raft) Start(command interface{}) (int, int, bool) {
    rf.mu.Lock()
    defer rf.mu.Unlock()

    if rf.state != Leader {
        return -1, -1, false
    }

    // ✅ CRITICAL: Compute LOGICAL index BEFORE appending
    // This is what the client expects to see in ApplyMsg
    index := rf.lastLogIndex() + 1  // Logical index
    term := rf.currentTerm

    entry := LogEntry{Term: term, Command: command}
    rf.log = append(rf.log, entry)

    rf.matchIndex[rf.me] = index
    rf.nextIndex[rf.me] = index + 1

    rf.persist()
    return index, term, true  // ✅ Correct logical index
}

// Helper function (line 708-712)
func (rf *Raft) lastLogIndex() int {
    // Returns LOGICAL index, not slice index
    return rf.lastIncludedIndex + len(rf.log) - 1
}
```

### What Breaks? (Step-by-Step Scenario)

**Setup:**
- Leader has processed 200 commands (indices 1-200)
- Service calls `Snapshot(150)` on leader
- Leader trims log: keeps entries 151-200 plus snapshot base
- Leader's state:
  - `lastIncludedIndex = 150`
  - `log = [base@150, entry151, entry152, ..., entry200]`
  - `len(log) = 51` (base + 50 entries)

**Client Call Sequence:**
1. **Client calls Start(cmd1):**
   - Buggy code: returns `len(log) = 51` ❌
   - Correct code: returns `lastLogIndex() + 1 = 200 + 1 = 201` ✅
2. **Leader commits entry:**
   - Entry committed at logical index 201
   - Leader sends ApplyMsg with CommandIndex = 201 to service
3. **Client waiting for index 51:**
   - Buggy code: Client waits for index 51 (already committed long ago!)
   - Client never sees their command committed
   - OR client sees WRONG command at index 51

**Consequence:** Client-service contract broken. Client can't correlate Start() response with ApplyMsg.

### Why This Violates API Contract

**From raft.go comments (lines 606-609):**
> "the first return value is the index that the command will appear at if it's ever committed"

**Client expectation:**
```go
index, term, ok := rf.Start(command)
// Later, client receives ApplyMsg:
msg := <-applyCh
if msg.CommandIndex == index && msg.Command == command {
    // SUCCESS: Our command committed
}
```

**With buggy code:** `msg.CommandIndex (201) != index (51)` → client never sees commit

### Index Translation Math (Critical for Snapshots)

**Invariant after snapshots:**
- `log[0]` represents `lastIncludedIndex` (snapshot base entry)
- `log[i]` represents logical index `lastIncludedIndex + i`

**Formula:**
```
logicalIndex = lastIncludedIndex + sliceIndex
sliceIndex = logicalIndex - lastIncludedIndex

lastLogIndex() = lastIncludedIndex + len(log) - 1
nextIndex = lastLogIndex() + 1
```

**Example:**
- `lastIncludedIndex = 100`
- `log = [base@100, entry101, entry102, entry103]`
- `len(log) = 4`
- `lastLogIndex() = 100 + 4 - 1 = 103`
- `Start()` should return `103 + 1 = 104`

### Reasoning Pattern (For Quiz)

**Question Template:** "After snapshot at index N, what index should Start() return?"

**Reasoning Steps:**
1. **Identify log state:** What is `lastIncludedIndex`? What is `log` contents?
2. **Calculate last entry:** `lastLogIndex() = lastIncludedIndex + len(log) - 1`
3. **Calculate next index:** `nextIndex = lastLogIndex() + 1`
4. **Verify client contract:** This is what client will see in ApplyMsg

**Answer:** Next logical index (lastLogIndex() + 1), NOT slice length.

### Code References
- **Start() function:** Lines 610-640
- **Index calculation:** Line 621 (`index := rf.lastLogIndex() + 1`)
- **lastLogIndex() helper:** Lines 708-712
- **Index translation:** Lines 671-676 (logIndexToSlice)
- **Snapshot() that creates gap:** Lines 202-245

### Quiz Exercise 1.3

**Scenario:** Leader has snapshotted through index 75. Current state:
- `lastIncludedIndex = 75`
- `log = [base@75, entry76, entry77, entry78]`
- Client calls `Start(cmd)`

**Questions:**
1. If Start() returns `len(log)`, what index does client receive? (Buggy version)
2. What is the correct logical index for this new entry?
3. Client waits for ApplyMsg with CommandIndex from Start(). Which scenario breaks?
4. After this Start(), what is new `len(log)`? What is new `lastLogIndex()`?
5. If another Snapshot(77) is called immediately after, what happens to the log?

**Answers:**
1. Client receives `4` (length of log array)
2. Correct logical index: `75 + 4 - 1 + 1 = 79` OR `lastLogIndex()+1 = 78+1 = 79`
3. Client waits for CommandIndex=4 but ApplyMsg will have CommandIndex=79. Client never matches.
4. New `len(log) = 5`, new `lastLogIndex() = 75 + 5 - 1 = 79`
5. Snapshot(77) trims entries ≤77: log becomes `[base@77, entry78, entry79]`, `lastIncludedIndex=77`, `len(log)=3`

---

## Summary: The 3 Critical Bugs

| Bug | Type | Figure 2 Rule | Code Line | Quiz Probability |
|-----|------|---------------|-----------|------------------|
| Double-voting | Safety | One vote per term | 387 | ⭐⭐⭐ VERY HIGH |
| Unstable timeout | Liveness | Election timing | 665-668 | ⭐⭐ HIGH |
| Start() index | Contract | Index semantics | 621 | ⭐⭐⭐ VERY HIGH (3D-specific) |

**Master these 3 bugs and you're 80% prepared for quiz scenarios.**

---


# PART 2: Figure 2 Deep Dive with Implementation Mapping

## Why This Section Matters

Figure 2 from the extended RAFT paper is **the specification**. Every quiz question ultimately tests whether you understand a Figure 2 rule. This section maps every rule to your exact implementation lines.

**Study Strategy:**
1. Print Figure 2 from the paper
2. Annotate with line numbers from this section
3. For each rule, practice explaining "what breaks if we violate this?"

---

## Figure 2 Structure Overview

Figure 2 contains:
- **State (All Servers):** Persistent + Volatile variables
- **State (Leaders):** Leader-specific volatile variables
- **AppendEntries RPC:** Arguments, Results, Receiver/Sender logic
- **RequestVote RPC:** Arguments, Results, Receiver/Sender logic
- **Rules for Servers:** All Servers, Followers, Candidates, Leaders

---

## State Variables Mapping

### Persistent State (Must survive crashes)

| Figure 2 Variable | Code Location | Implementation Notes | Why Persistent |
|-------------------|---------------|----------------------|----------------|
| `currentTerm` | Line 52 | Latest term seen | Prevents accepting stale leaders after crash |
| `votedFor` | Line 53 | Candidate voted for (or -1) | Prevents double-voting after crash |
| `log[]` | Line 54 | Array of LogEntry{Term, Command} | Safety: committed entries must survive |

**Persistence Implementation:**
- **Encoding:** Lines 97-117 (`persist()` function uses labgob encoder)
- **Decoding:** Lines 120-158 (`readPersist()` function)
- **Snapshot support:** Lines 224-232 (persist() includes snapshot bytes)
- **Call sites:** Every function that modifies persistent state (6+ locations)

**Critical Rule:** Must persist BEFORE replying to RPCs (not after!)
- **RequestVote:** Line 305 (persist before setting reply.VoteGranted=true)
- **AppendEntries:** Line 391 (persist before processing log entries)

### Volatile State on All Servers

| Figure 2 Variable | Code Location | Initial Value | Purpose |
|-------------------|---------------|---------------|---------|
| `commitIndex` | Line 57 | 0 | Highest log entry known committed |
| `lastApplied` | Line 58 | 0 | Highest log entry applied to state machine |

**Invariant:** `lastApplied ≤ commitIndex ≤ lastLogIndex()`

**Update Logic:**
- `commitIndex` updated by: Leader (line 754 in advanceCommitIndex), Follower (line 497 from leaderCommit)
- `lastApplied` updated by: Applier goroutine (line 976-995)

### Volatile State on Leaders (Reinitialized after election)

| Figure 2 Variable | Code Location | Reinitialization | Purpose |
|-------------------|---------------|------------------|---------|
| `nextIndex[]` | Line 62 | lastLogIndex() + 1 for all servers | Next log entry to send to each follower |
| `matchIndex[]` | Line 63 | 0 for all servers | Highest log entry known replicated on each server |

**Reinitialization Location:** Lines 1045-1050 (when server becomes leader in ticker())

**Update Logic:**
- `nextIndex[i]` decremented on failure (line 890), set to matchIndex+1 on success
- `matchIndex[i]` updated on success (line 893)

### Snapshot State (Phase 3D)

| Variable | Code Location | Purpose |
|----------|---------------|---------|
| `lastIncludedIndex` | Line 67 | Index of last entry in snapshot |
| `lastIncludedTerm` | Line 68 | Term of last entry in snapshot |
| `snapshot` | Line 69 | Snapshot bytes from service |

**Critical:** These enable log compaction without losing committed entries

---

## AppendEntries RPC Mapping

### AppendEntries Arguments (Line 348-355)

| Figure 2 Field | Type | Purpose |
|----------------|------|---------|
| `term` | int | Leader's term |
| `leaderId` | int | For follower to redirect clients |
| `prevLogIndex` | int | Index of log entry immediately preceding new ones |
| `prevLogTerm` | int | Term of prevLogIndex entry |
| `entries[]` | []LogEntry | Log entries to store (empty for heartbeat) |
| `leaderCommit` | int | Leader's commitIndex |

**Heartbeat:** `entries[]` is empty, but prevLogIndex/prevLogTerm still used for consistency check

### AppendEntries Reply (Line 359-368)

| Figure 2 Field | Type | Purpose |
|----------------|------|---------|
| `term` | int | currentTerm, for leader to update itself |
| `success` | bool | true if follower matched prevLogIndex and prevLogTerm |
| `XTerm` | int | (Extension) Term of conflicting entry |
| `XIndex` | int | (Extension) Index of first entry with XTerm |
| `XLen` | int | (Extension) Log length (when too short) |

**Fast Backup Optimization:** XTerm/XIndex/XLen are NOT in basic Figure 2 but are from paper page 8 (gray box)

### AppendEntries Receiver Implementation (Lines 372-510)

**Figure 2 Rules:**

#### Rule 1: Reply false if term < currentTerm (Line 377)
```go
if args.Term < rf.currentTerm {
    reply.Term = rf.currentTerm
    reply.Success = false
    return
}
```
**Why:** Reject stale leaders

#### Rule 2: Reply false if log doesn't contain prevLogIndex with prevLogTerm (Lines 406-443)

**Case 1: prevLogIndex in snapshot (leader behind) - Line 406**
```go
if args.PrevLogIndex < rf.lastIncludedIndex {
    reply.Success = false
    reply.XLen = rf.lastIncludedIndex
    return
}
```

**Case 2: Log too short - Line 416**
```go
if args.PrevLogIndex > rf.lastLogIndex() {
    reply.Success = false
    reply.XLen = rf.lastLogIndex() + 1  // Fast backup hint
    return
}
```

**Case 3: Term mismatch - Line 428**
```go
prevLogTerm := rf.getTermAtIndex(args.PrevLogIndex)
if prevLogTerm != args.PrevLogTerm {
    reply.Success = false
    reply.XTerm = prevLogTerm
    // Find first index of XTerm
    reply.XIndex = args.PrevLogIndex
    for reply.XIndex > rf.lastIncludedIndex &&
        rf.getTermAtIndex(reply.XIndex-1) == reply.XTerm {
        reply.XIndex--
    }
    return
}
```

**Why This Rule Matters:** Ensures logs are consistent before appending (safety property)

#### Rule 3: Delete conflicting entries (Lines 446-480)
```go
for i, entry := range args.Entries {
    logicalIndex := args.PrevLogIndex + 1 + i
    if logicalIndex <= rf.lastIncludedIndex {
        continue  // Already in snapshot
    }
    if rf.hasLogEntry(logicalIndex) {
        existingTerm := rf.getTermAtIndex(logicalIndex)
        if existingTerm != entry.Term {
            // Conflict: delete this and all following
            sliceIndex := rf.logIndexToSlice(logicalIndex)
            rf.log = rf.log[:sliceIndex]
            rf.log = append(rf.log, entry)
        }
    } else {
        // Entry doesn't exist: append
        rf.log = append(rf.log, entry)
    }
}
```

**Why:** If existing entry conflicts, new leader's log is authoritative

#### Rule 4: Append new entries not in log (Lines 471-479)
(Handled in same loop as Rule 3)

#### Rule 5: Update commitIndex if leaderCommit > commitIndex (Lines 490-499)
```go
if args.LeaderCommit > rf.commitIndex {
    // Set commitIndex = min(leaderCommit, index of last new entry)
    newCommitIndex := args.LeaderCommit
    lastNewEntryIndex := rf.lastLogIndex()
    if lastNewEntryIndex < newCommitIndex {
        newCommitIndex = lastNewEntryIndex
    }
    rf.commitIndex = newCommitIndex
}
```

**Why:** Follower learns which entries are committed from leader

### AppendEntries Sender Logic (Lines 760-920)

**Figure 2 Rule: Leaders - Upon election**
- Initialize nextIndex[] to lastLogIndex() + 1 (Line 1046)
- Initialize matchIndex[] to 0 (Line 1049)

**Figure 2 Rule: Leaders - Upon receiving Start()**
- Append entry to local log (Line 629)
- Send AppendEntries to all peers (triggered by heartbeat goroutine)

**Figure 2 Rule: Leaders - Send heartbeats during idle periods**
- Heartbeat loop (Line 764): Every 100ms
- Branch: InstallSnapshot if nextIndex ≤ lastIncludedIndex (Line 793)
- Otherwise: Send AppendEntries (Line 821)

**On Success (Lines 886-897):**
```go
if reply.Success {
    // Update nextIndex and matchIndex
    newMatchIndex := args.PrevLogIndex + len(args.Entries)
    rf.nextIndex[server] = newMatchIndex + 1
    rf.matchIndex[server] = newMatchIndex
    
    // Try to advance commitIndex
    rf.advanceCommitIndex()
}
```

**On Failure (Lines 858-875): Fast Backup**
```go
if !reply.Success {
    if reply.XTerm == -1 {
        // Log too short
        rf.nextIndex[server] = reply.XLen
    } else {
        // Find last entry in leader's log with term XTerm
        lastIndexOfXTerm := -1
        for i := args.PrevLogIndex; i > rf.lastIncludedIndex; i-- {
            if rf.getTermAtIndex(i) == reply.XTerm {
                lastIndexOfXTerm = i
                break
            }
        }
        if lastIndexOfXTerm != -1 {
            rf.nextIndex[server] = lastIndexOfXTerm + 1
        } else {
            rf.nextIndex[server] = reply.XIndex
        }
    }
}
```

---

## RequestVote RPC Mapping

### RequestVote Arguments (Line 250-255)

| Figure 2 Field | Type | Purpose |
|----------------|------|---------|
| `term` | int | Candidate's term |
| `candidateId` | int | Candidate requesting vote |
| `lastLogIndex` | int | Index of candidate's last log entry |
| `lastLogTerm` | int | Term of candidate's last log entry |

### RequestVote Reply (Line 259-262)

| Figure 2 Field | Type | Purpose |
|----------------|------|---------|
| `term` | int | currentTerm, for candidate to update itself |
| `voteGranted` | bool | true if candidate received vote |

### RequestVote Receiver Implementation (Lines 266-312)

**Figure 2 Rules:**

#### Rule 1: Reply false if term < currentTerm (Line 271)
```go
if args.Term < rf.currentTerm {
    reply.Term = rf.currentTerm
    reply.VoteGranted = false
    return
}
```

#### Rule 2: Election Restriction - Vote if candidate's log is up-to-date (Lines 290-297)
```go
logIsUpToDate := false
if args.LastLogTerm > lastLogTerm {
    logIsUpToDate = true
} else if args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex {
    logIsUpToDate = true
}
```

**Definition of "up-to-date" (Section 5.4.1):**
- If logs have different last term: higher term is more up-to-date
- If logs end with same term: longer log is more up-to-date

**Why Critical:** Ensures new leader has all committed entries

#### Rule 3: Grant vote if votedFor is null/candidateId AND log up-to-date (Lines 302-311)
```go
if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && logIsUpToDate {
    rf.votedFor = args.CandidateId
    rf.resetElectionTimer()  // Reset timer when granting vote
    rf.persist()             // Persist vote BEFORE replying
    reply.VoteGranted = true
} else {
    reply.VoteGranted = false
}
```

### RequestVote Sender Logic (Lines 1019-1040)

**Figure 2 Rule: Candidates - On conversion to candidate**
- Increment currentTerm (Line 1019)
- Vote for self (Line 1020)
- Reset election timer (Line 1021)
- Send RequestVote RPCs to all servers (Line 1024)

**Figure 2 Rule: Candidates - Become leader if receive votes from majority (Lines 1036-1040)**
```go
if voteCount > len(rf.peers)/2 {
    rf.state = Leader
    // Initialize nextIndex[] and matchIndex[]
    // Start heartbeat goroutine
}
```

---

## Rules for Servers Mapping

### All Servers Rules

#### Rule 1: If commitIndex > lastApplied, apply log[lastApplied] (Lines 960-995)
**Implementation:** Applier goroutine
```go
for !rf.killed() {
    rf.mu.Lock()
    for rf.lastApplied < rf.commitIndex {
        rf.lastApplied++
        entry := rf.log[rf.logIndexToSlice(rf.lastApplied)]
        msg := ApplyMsg{
            CommandValid: true,
            Command:      entry.Command,
            CommandIndex: rf.lastApplied,
        }
        rf.mu.Unlock()
        rf.applyCh <- msg  // Send to service (MUST release lock first!)
        rf.mu.Lock()
    }
    rf.mu.Unlock()
    time.Sleep(10 * time.Millisecond)
}
```

**Critical:** Release lock before channel send to avoid deadlock

#### Rule 2: If RPC request/response contains term T > currentTerm (Lines 279, 387)
```go
if args.Term > rf.currentTerm {
    rf.currentTerm = args.Term
    rf.votedFor = -1
    rf.state = Follower
    rf.persist()
}
```

**Called in:** RequestVote handler, AppendEntries handler, all RPC reply handlers

### Followers Rules

#### Rule: Reset election timer on valid AppendEntries or granting vote
- **AppendEntries:** Line 400 (`rf.resetElectionTimer()`)
- **RequestVote (grant):** Line 304 (`rf.resetElectionTimer()`)

**Why:** Prevents unnecessary elections while leader is alive

### Candidates Rules

#### Rule 1: On conversion, start election (Lines 1013-1030 in ticker())

#### Rule 2: If votes from majority → become leader (Lines 1036-1050)

#### Rule 3: If AppendEntries from new leader → convert to follower (Line 392)
```go
} else if rf.state == Candidate || rf.state == Leader {
    // If we're candidate/leader and receive AppendEntries with equal term,
    // recognize the leader and step down
    rf.state = Follower
}
```

#### Rule 4: If election timeout → start new election (Line 1007-1013)

### Leaders Rules

#### Rule 1: Upon election, initialize leader state (Lines 1041-1050)
```go
// Initialize nextIndex[] to last log index + 1
for i := range rf.nextIndex {
    rf.nextIndex[i] = rf.lastLogIndex() + 1
}
// Initialize matchIndex[] to 0
for i := range rf.matchIndex {
    rf.matchIndex[i] = 0
}
// Self-bookkeeping
rf.matchIndex[rf.me] = rf.lastLogIndex()
```

#### Rule 2: Upon receiving Start() → append to local log (Lines 619-640)

#### Rule 3: Send periodic heartbeats (Lines 760-920)

#### Rule 4: If there exists N where majority matchIndex[i] ≥ N (Lines 723-758)

**advanceCommitIndex() implementation:**
```go
for N := rf.lastLogIndex(); N > rf.commitIndex && N > rf.lastIncludedIndex; N-- {
    // CRITICAL: Only commit entries from currentTerm
    if rf.getTermAtIndex(N) != rf.currentTerm {
        continue
    }
    
    count := 0
    for i := range rf.peers {
        if rf.matchIndex[i] >= N {
            count++
        }
    }
    
    if count > len(rf.peers)/2 {
        rf.commitIndex = N
        break
    }
}
```

**Why currentTerm check:** Figure 8 scenario - prevents committing old-term entries by counting

---

## Figure 2 Compliance Checklist

Use this to verify your implementation follows Figure 2 exactly:

- ✅ **Persistent state:** currentTerm, votedFor, log[] (Line 52-54)
- ✅ **Volatile state (all):** commitIndex, lastApplied (Line 57-58)
- ✅ **Volatile state (leaders):** nextIndex[], matchIndex[] (Line 62-63)
- ✅ **AppendEntries args:** term, leaderId, prevLogIndex, prevLogTerm, entries[], leaderCommit (Line 348-355)
- ✅ **AppendEntries reply:** term, success (Line 359-362)
- ✅ **RequestVote args:** term, candidateId, lastLogIndex, lastLogTerm (Line 250-255)
- ✅ **RequestVote reply:** term, voteGranted (Line 259-262)
- ✅ **AE receiver rule 1:** Reject if term < currentTerm (Line 377)
- ✅ **AE receiver rule 2:** Consistency check (Lines 406-443)
- ✅ **AE receiver rule 3-4:** Delete conflicts, append new (Lines 446-480)
- ✅ **AE receiver rule 5:** Update commitIndex from leaderCommit (Line 490-499)
- ✅ **RV receiver rule 1:** Reject if term < currentTerm (Line 271)
- ✅ **RV receiver rule 2:** Election restriction (Lines 290-297)
- ✅ **All servers rule 1:** Apply committed entries (Lines 960-995)
- ✅ **All servers rule 2:** Update term if higher (Lines 279, 387)
- ✅ **Followers rule:** Reset timer on heartbeat/vote (Lines 304, 400)
- ✅ **Candidates rule 1:** Start election on timeout (Line 1007-1030)
- ✅ **Candidates rule 2:** Become leader on majority (Line 1036-1050)
- ✅ **Leaders rule 1:** Initialize nextIndex/matchIndex (Line 1041-1050)
- ✅ **Leaders rule 2:** Append to log on Start() (Line 619-640)
- ✅ **Leaders rule 3:** Send heartbeats (Line 760-920)
- ✅ **Leaders rule 4:** Advance commitIndex (Line 723-758)

---

## Quiz Exercise 2.1: Figure 2 Violation Detection

For each scenario, identify which Figure 2 rule is violated:

1. **Scenario:** Leader commits entry from term 3 by counting replicas (majority has it)
   - **Violated rule:** Leaders rule 4 (only commit currentTerm entries)
   - **Code location:** Line 740 check missing
   - **Consequence:** Figure 8 scenario - entry might be overwritten

2. **Scenario:** Server processes AppendEntries but doesn't reset election timer
   - **Violated rule:** Followers rule (reset timer on valid RPC)
   - **Code location:** Line 400 missing
   - **Consequence:** Unnecessary elections

3. **Scenario:** Candidate grants vote without checking log up-to-date
   - **Violated rule:** RequestVote receiver rule 2 (election restriction)
   - **Code location:** Lines 290-297 check missing
   - **Consequence:** Leader elected without all committed entries → data loss

4. **Scenario:** Server persists state AFTER sending RPC reply
   - **Violated rule:** Implicit in persistent state definition (must persist BEFORE reply)
   - **Code location:** Lines 305, 391 order swapped
   - **Consequence:** After crash, server forgets vote/term → double-voting

5. **Scenario:** Start() returns slice length instead of logical index
   - **Violated rule:** API contract (not strictly Figure 2, but interface definition)
   - **Code location:** Line 621 uses wrong formula
   - **Consequence:** Client can't match Start() index with ApplyMsg

---


# PART 3: Index Translation Mastery (Snapshot Arithmetic)

## Why This Section Matters

**Index translation is THE most error-prone aspect of Phase 3D.** After implementing snapshots, your log array no longer represents the full log history—it's a sliding window. Every log access must account for this.

**Common quiz traps:**
- "What index does Start() return after snapshot?"
- "Which log entry is at log[2] after Snapshot(100)?"
- "What happens if you forget to translate in function X?"

**Master the math in this section and you'll ace 3D quiz questions.**

---

## The Core Problem: Two Index Spaces

###Before Snapshots (Phase 3A-3C):
```
Logical Index Space:    0    1    2    3    4    5    6    7    8
log[] Array:          [dummy, e1,  e2,  e3,  e4,  e5,  e6,  e7,  e8]
                        ↑
                     log[0] = dummy entry (term 0)
```

**Simple mapping:** `log[i]` represents entry at logical index `i`

### After Snapshot(5) (Phase 3D):
```
Logical Index Space:    0    1    2    3    4    5  |  6    7    8
                                            SNAPSHOT  |
log[] Array:                              [base@5, e6,  e7,  e8]
                                             ↑     ↑    ↑    ↑
                                          log[0] log[1] log[2] log[3]

lastIncludedIndex = 5
Entries 0-5 are in snapshot (not in log array)
```

**Complex mapping:** `log[i]` represents entry at logical index `lastIncludedIndex + i`

---

## The Translation Formula

### Core Formula (Line 675)
```go
sliceIndex = logicalIndex - lastIncludedIndex
```

**Example:**
- Want entry at logical index 7
- `lastIncludedIndex = 5`
- `sliceIndex = 7 - 5 = 2`
- Access: `log[2]`

### Inverse Formula
```go
logicalIndex = lastIncludedIndex + sliceIndex
```

**Example:**
- Looking at `log[2]`
- `lastIncludedIndex = 5`
- `logicalIndex = 5 + 2 = 7`
- This array element represents entry 7

### Last Log Index (Line 712)
```go
lastLogIndex() = lastIncludedIndex + len(log) - 1
```

**Why minus 1?** `log[0]` is the snapshot base entry at `lastIncludedIndex`, so:
- `log[0]` → index `lastIncludedIndex`
- `log[len-1]` → index `lastIncludedIndex + len - 1`

**Example:**
- `lastIncludedIndex = 5`
- `log = [base@5, e6, e7, e8]` (length 4)
- `lastLogIndex() = 5 + 4 - 1 = 8` ✓

---

## The Snapshot Base Entry Invariant

**Invariant:** After any snapshot, `log[0]` represents the snapshot boundary.

```go
type LogEntry struct {
    Term    int         // Term of the snapshot point
    Command interface{} // nil for snapshot base entries
}
```

**After Snapshot(index):**
- `log[0] = LogEntry{Term: termAtIndex(index), Command: nil}`
- This allows `getTermAtIndex(lastIncludedIndex)` to simply return `log[0].Term`

**Why this matters:**
- Simplifies term lookups at snapshot boundary
- Maintains `log[0]` exists invariant (no bounds checking needed)
- Makes code cleaner than tracking "empty log after snapshot"

---

## Helper Functions Reference

### 1. logIndexToSlice(logicalIndex int) int - Line 674-676
```go
func (rf *Raft) logIndexToSlice(logicalIndex int) int {
    return logicalIndex - rf.lastIncludedIndex
}
```

**Purpose:** Convert logical index → array index
**Precondition:** Caller must verify `logicalIndex >= lastIncludedIndex`
**Usage:** Before accessing `log[...]`

### 2. hasLogEntry(logicalIndex int) bool - Line 678-690
```go
func (rf *Raft) hasLogEntry(logicalIndex int) bool {
    if logicalIndex <= rf.lastIncludedIndex {
        return false  // Entry is in snapshot
    }
    if logicalIndex > rf.lastLogIndex() {
        return false  // Entry doesn't exist yet
    }
    return true
}
```

**Purpose:** Check if entry exists in log array (not snapshot, not future)
**Safe pattern:** Always check before calling `logIndexToSlice()`

### 3. getTermAtIndex(logicalIndex int) int - Line 693-706
```go
func (rf *Raft) getTermAtIndex(logicalIndex int) int {
    if logicalIndex == rf.lastIncludedIndex {
        return rf.lastIncludedTerm  // Or log[0].Term
    }
    if logicalIndex < rf.lastIncludedIndex {
        panic("Requesting term for compacted entry")
    }
    sliceIndex := rf.logIndexToSlice(logicalIndex)
    if sliceIndex < 0 || sliceIndex >= len(rf.log) {
        panic("Index out of bounds")
    }
    return rf.log[sliceIndex].Term
}
```

**Purpose:** Get term at logical index (handles snapshot boundary)
**Panics:** If asking for compacted entry (should never happen in correct code)

### 4. lastLogIndex() int - Line 711-713
```go
func (rf *Raft) lastLogIndex() int {
    return rf.lastIncludedIndex + len(rf.log) - 1
}
```

**Purpose:** Returns LOGICAL index of last entry
**Critical:** Returns logical index, NOT slice index!

### 5. lastLogTerm() int - Line 718-721
```go
func (rf *Raft) lastLogTerm() int {
    idx := rf.lastLogIndex()
    return rf.getTermAtIndex(idx)
}
```

**Purpose:** Get term of last entry (uses getTermAtIndex for safety)

---

## Practice Exercises: 10 Scenarios

### Exercise 3.1: Basic Translation

**Setup:**
- `lastIncludedIndex = 50`
- `log = [base@50, entry51, entry52, entry53]`
- `len(log) = 4`

**Questions:**
1. What is `lastLogIndex()`?
2. Where is entry 52 in the array?
3. What is `log[2].Term` if entry 52 has term 7?
4. If you want to access entry 51, what sliceIndex?

**Answers:**
1. `lastLogIndex() = 50 + 4 - 1 = 53`
2. `sliceIndex = 52 - 50 = 2`, so `log[2]`
3. `log[2].Term = 7`
4. `logIndexToSlice(51) = 51 - 50 = 1`, so `log[1]`

---

### Exercise 3.2: Start() Index Calculation

**Setup:**
- `lastIncludedIndex = 100`
- `log = [base@100, entry101]`
- Client calls `Start(cmd)`

**Questions:**
1. What is `lastLogIndex()` before Start()?
2. What index should Start() return?
3. After appending, what is new `lastLogIndex()`?
4. Where is the new entry in the log array?

**Answers:**
1. `lastLogIndex() = 100 + 2 - 1 = 101`
2. `Start()` returns `lastLogIndex() + 1 = 101 + 1 = 102`
3. New `lastLogIndex() = 100 + 3 - 1 = 102`
4. New entry is at `log[2]` (slice index 2)

---

### Exercise 3.3: Snapshot Impact

**Setup:**
- `lastIncludedIndex = 0` (no snapshot yet)
- `log = [dummy@0, e1, e2, e3, e4, e5, e6, e7, e8, e9, e10]`
- Service calls `Snapshot(7)`

**Questions:**
1. What is new `lastIncludedIndex`?
2. What entries remain in log after snapshot?
3. What is new `len(log)`?
4. What is new `lastLogIndex()`?
5. Where is entry 8 in the new log array?

**Answers:**
1. `lastIncludedIndex = 7`
2. `log = [base@7, e8, e9, e10]` (entries AFTER index 7)
3. `len(log) = 4`
4. `lastLogIndex() = 7 + 4 - 1 = 10` (unchanged, only representation changed)
5. Entry 8 is at `log[1]` (sliceIndex = 8 - 7 = 1)

---

### Exercise 3.4: AppendEntries Consistency Check

**Setup (Follower):**
- `lastIncludedIndex = 30`
- `log = [base@30, e31, e32, e33]`
- Receives AppendEntries with `prevLogIndex = 32`

**Questions:**
1. Does follower have entry at index 32?
2. How do you check? (Use helper function)
3. To get term at index 32, what slice index?
4. If prevLogTerm matches, where do new entries start?

**Answers:**
1. Check: `hasLogEntry(32)` → `32 > 30 AND 32 <= lastLogIndex(33)` → true
2. Use `hasLogEntry(32)` first, then `getTermAtIndex(32)` if true
3. `sliceIndex = logIndexToSlice(32) = 32 - 30 = 2`, check `log[2].Term`
4. New entries start at index 33, append starting at `log[3]`

---

### Exercise 3.5: Edge Case - Snapshot Boundary

**Setup:**
- `lastIncludedIndex = 50`
- `log = [base@50, e51, e52]`
- Request: Get term at index 50

**Questions:**
1. Is index 50 in the log array?
2. Which function handles this case?
3. What does `getTermAtIndex(50)` return?
4. What would `hasLogEntry(50)` return?

**Answers:**
1. Yes, it's represented by `log[0]` (the base entry)
2. `getTermAtIndex()` handles this: `if logicalIndex == lastIncludedIndex`
3. Returns `rf.lastIncludedTerm` (or equivalently `log[0].Term`)
4. `hasLogEntry(50)` returns `false` because check is `logicalIndex <= lastIncludedIndex`

**Why false?** The entry is technically in the snapshot, not in the "active" log. `log[0]` is just metadata about the snapshot boundary.

---

### Exercise 3.6: advanceCommitIndex Bounds

**Setup (Leader):**
- `lastIncludedIndex = 100`
- `log = [base@100, e101, e102, e103]`
- `commitIndex = 101`
- Trying to advance commitIndex

**Questions:**
1. What range of indices does advanceCommitIndex scan?
2. Why does it check `N > lastIncludedIndex`?
3. If majority has replicated up to 103, what is new commitIndex?
4. What happens if we forget the `N > lastIncludedIndex` check?

**Answers:**
1. Scans from `lastLogIndex()=103` down to `commitIndex+1=102`, stopping if `N <= lastIncludedIndex`
2. Can't commit entries in snapshot (already committed by definition)
3. New `commitIndex = 103` (if entry at 103 has currentTerm)
4. Could try to access `log[sliceIndex < 0]` → panic or wrong entry

**Code (Line 735):**
```go
for N := rf.lastLogIndex(); N > rf.commitIndex && N > rf.lastIncludedIndex; N-- {
    // ...
}
```

---

### Exercise 3.7: Multiple Snapshots

**Setup:**
- Start: `lastIncludedIndex = 0`, `log = [dummy, e1, ..., e20]`
- Operation 1: `Snapshot(10)`
- Operation 2: `Snapshot(15)`

**Questions (After Op 2):**
1. What is final `lastIncludedIndex`?
2. What is final `log` contents?
3. What is final `lastLogIndex()`?
4. Where is entry 16 in the array?
5. What happened to entries 11-14?

**Answers:**
1. `lastIncludedIndex = 15`
2. `log = [base@15, e16, e17, e18, e19, e20]` (length 6)
3. `lastLogIndex() = 15 + 6 - 1 = 20`
4. Entry 16 is at `log[1]` (sliceIndex = 16 - 15 = 1)
5. Entries 11-14 were included in first snapshot, then trimmed again in second snapshot

---

### Exercise 3.8: Leader Sending AppendEntries

**Setup (Leader):**
- `lastIncludedIndex = 50`
- `log = [base@50, e51, e52, e53, e54, e55]`
- `nextIndex[follower] = 52`
- Sending AppendEntries to follower

**Questions:**
1. What is `prevLogIndex` for this AppendEntries?
2. How do you find the term at prevLogIndex?
3. What entries should be sent?
4. If `nextIndex[follower] = 49`, what should leader send instead?

**Answers:**
1. `prevLogIndex = nextIndex[follower] - 1 = 52 - 1 = 51`
2. `prevLogTerm = getTermAtIndex(51)` → `log[logIndexToSlice(51)] = log[1].Term`
3. Send entries from index 52 onwards: `[e52, e53, e54, e55]`
4. If `nextIndex = 49 < lastIncludedIndex(50)`, send `InstallSnapshot` instead (Line 793)

**Code Pattern (Line 821-840):**
```go
prevLogIndex := rf.nextIndex[server] - 1
prevLogTerm := rf.getTermAtIndex(prevLogIndex)  // Handles snapshot boundary

// Build entries array starting from nextIndex[server]
entries := []LogEntry{}
for i := rf.nextIndex[server]; i <= rf.lastLogIndex(); i++ {
    sliceIdx := rf.logIndexToSlice(i)
    entries = append(entries, rf.log[sliceIdx])
}
```

---

### Exercise 3.9: Follower Truncating Log

**Setup (Follower):**
- `lastIncludedIndex = 20`
- `log = [base@20, e21(T3), e22(T3), e23(T4), e24(T4)]`
- Receives AppendEntries:
  - `prevLogIndex = 22`
  - `prevLogTerm = 3` (matches!)
  - `entries = [e23(T5), e24(T5)]` (conflict at 23!)

**Questions:**
1. Where does conflict detection happen?
2. What slice index corresponds to logical index 23?
3. After detecting conflict, what should follower do?
4. What is final log contents?

**Answers:**
1. Loop through received entries, check existing entries for term mismatch (Line 446-480)
2. `sliceIndex = logIndexToSlice(23) = 23 - 20 = 3`, check `log[3].Term`
3. Detect `log[3].Term (4) != entry.Term (5)`, truncate: `log = log[:3]`, then append new entries
4. Final: `log = [base@20, e21(T3), e22(T3), e23(T5), e24(T5)]`

**Code Pattern (Line 456-469):**
```go
logicalIndex := args.PrevLogIndex + 1 + i
if rf.hasLogEntry(logicalIndex) {
    existingTerm := rf.getTermAtIndex(logicalIndex)
    if existingTerm != entry.Term {
        // Conflict: truncate and append
        sliceIndex := rf.logIndexToSlice(logicalIndex)
        rf.log = rf.log[:sliceIndex]
        rf.log = append(rf.log, entry)
    }
} else {
    // No conflict: just append
    rf.log = append(rf.log, entry)
}
```

---

### Exercise 3.10: Quiz Trap - Forgetting Translation

**Scenario:** Buggy advanceCommitIndex() implementation:
```go
// BUGGY CODE
func (rf *Raft) advanceCommitIndex() {
    for N := len(rf.log) - 1; N > rf.commitIndex; N-- {  // ❌ Using slice index!
        if rf.log[N].Term != rf.currentTerm {
            continue
        }
        count := 0
        for i := range rf.peers {
            if rf.matchIndex[i] >= N {  // ❌ Comparing logical with slice index!
                count++
            }
        }
        if count > len(rf.peers)/2 {
            rf.commitIndex = N  // ❌ Setting to slice index!
        }
    }
}
```

**Questions:**
1. Given `lastIncludedIndex=100`, `log=[base@100, e101, e102, e103]`, what is `len(log)-1`?
2. What logical indices should be checked?
3. What will buggy code check instead?
4. If majority has `matchIndex[i]=103`, will buggy code advance commitIndex?
5. What will commitIndex be set to?

**Answers:**
1. `len(log)-1 = 4-1 = 3` (slice index)
2. Should check logical indices 103, 102 (down from `lastLogIndex()`)
3. Buggy code checks slice indices 3, 2, 1 (down from `len-1`)
4. No! It checks if `matchIndex[i] (103) >= N (3)` → true for all, but N=3 is WRONG index
5. `commitIndex = 3` ← CATASTROPHIC! This is a slice index, not logical index. Applier will try to apply wrong entries.

**Correct Code (Line 735):**
```go
for N := rf.lastLogIndex(); N > rf.commitIndex && N > rf.lastIncludedIndex; N-- {
    // N is LOGICAL index
    if rf.getTermAtIndex(N) != rf.currentTerm {  // Use helper for term lookup
        continue
    }
    // ... rest of logic uses N as logical index
}
```

---

## Common Patterns to Remember

### Pattern 1: Accessing Log Entry
```go
// ✅ CORRECT
if rf.hasLogEntry(logicalIndex) {
    term := rf.getTermAtIndex(logicalIndex)
    sliceIdx := rf.logIndexToSlice(logicalIndex)
    command := rf.log[sliceIdx].Command
}

// ❌ WRONG
term := rf.log[logicalIndex].Term  // Assumes no snapshot!
```

### Pattern 2: Iterating Over Log
```go
// ✅ CORRECT (forward)
for i := rf.lastIncludedIndex + 1; i <= rf.lastLogIndex(); i++ {
    sliceIdx := rf.logIndexToSlice(i)
    entry := rf.log[sliceIdx]
    // Process entry at logical index i
}

// ✅ CORRECT (backward)
for N := rf.lastLogIndex(); N > rf.lastIncludedIndex; N-- {
    term := rf.getTermAtIndex(N)  // Use helper
    // Process entry at logical index N
}

// ❌ WRONG
for i := 0; i < len(rf.log); i++ {
    entry := rf.log[i]  // What logical index is this?
}
```

### Pattern 3: Returning Indices to Client
```go
// ✅ CORRECT
index := rf.lastLogIndex() + 1  // Next LOGICAL index
return index, term, true

// ❌ WRONG
index := len(rf.log)  // Slice length, not logical index!
return index, term, true
```

---

## Index Translation Quick Reference

| Operation | Formula | Example (lastIncludedIndex=50, log=[base, e51, e52, e53]) |
|-----------|---------|----------------------------------------------------------|
| Logical → Slice | `slice = logical - lastIncludedIndex` | 52 → 52-50 = 2 |
| Slice → Logical | `logical = slice + lastIncludedIndex` | 2 → 2+50 = 52 |
| Last log index | `lastIncludedIndex + len(log) - 1` | 50 + 4 - 1 = 53 |
| Next index after append | `lastLogIndex() + 1` | 53 + 1 = 54 |
| Check entry exists | `logical > lastIncludedIndex && logical <= lastLogIndex()` | 52: true, 49: false |

---

## Quiz Reasoning Pattern for Index Questions

**When you see an index question:**
1. **Identify the space:** Is this a logical index or slice index?
2. **Check snapshot state:** What is `lastIncludedIndex`? What is `log` contents?
3. **Apply formula:** Use appropriate translation formula
4. **Verify bounds:** Is the index in snapshot, in log, or beyond log?
5. **Use helpers:** Always use `logIndexToSlice()`, `hasLogEntry()`, `getTermAtIndex()` in your reasoning

**Common quiz tricks:**
- Mixing logical and slice indices in comparisons
- Forgetting `-1` in `lastLogIndex()` formula
- Accessing log without checking `hasLogEntry()` first
- Returning slice index to client instead of logical index

---


# PART 4: Concurrency & Race Reasoning

## Why This Section Matters

RAFT is a concurrent system with multiple goroutines accessing shared state. **Every quiz about "what breaks if X?" tests your understanding of race conditions and synchronization.**

**Key insight:** Without locks, RAFT would violate both safety (wrong behavior) and liveness (no progress).

---

## Lock Discipline: The Universal Pattern

**Every function that accesses shared state follows this pattern:**

```go
func (rf *Raft) SomeFunction() {
    rf.mu.Lock()           // Acquire lock FIRST
    defer rf.mu.Unlock()   // Release automatically on return
    
    // Access shared state safely
    term := rf.currentTerm
    rf.votedFor = candidateId
    rf.persist()
}
```

**Why `defer`?** Ensures unlock even if function returns early or panics.

---

## Where Races Would Occur WITHOUT Locks

### Race 1: Election Timer vs AppendEntries Handler

**Scenario:** Ticker goroutine reads `lastHeartbeat` while AppendEntries handler writes it

**Without locks:**
```
Ticker (reading):              AppendEntries (writing):
time.Since(rf.lastHeartbeat)   rf.lastHeartbeat = time.Now()
        ^                              ^
        |______ DATA RACE! ____________|
```

**Consequence:** 
- Ticker might read partially-written timestamp → corrupt value
- Go scheduler could interleave operations → undefined behavior

**Solution (Lines 400, 1007):**
```go
// In AppendEntries handler
rf.mu.Lock()
rf.lastHeartbeat = time.Now()  // Protected write
rf.mu.Unlock()

// In ticker
rf.mu.Lock()
timeSince := time.Since(rf.lastHeartbeat)  // Protected read
rf.mu.Unlock()
```

### Race 2: Multiple RequestVote Handlers (Concurrent RPCs)

**Scenario:** Two candidates send RequestVote simultaneously

**Without locks:**
```
Handler 1 (Candidate A):       Handler 2 (Candidate B):
if rf.votedFor == -1           if rf.votedFor == -1
    rf.votedFor = 2                rf.votedFor = 3
    reply.VoteGranted = true       reply.VoteGranted = true
```

**Consequence:** Both handlers see `votedFor == -1`, both grant vote → double-voting!

**Solution (Lines 267-312):**
```go
func (rf *Raft) RequestVote(args, reply) {
    rf.mu.Lock()
    defer rf.mu.Unlock()  // Entire handler is critical section
    
    // Check-then-act is atomic with lock held
    if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && logUpToDate {
        rf.votedFor = args.CandidateId
        reply.VoteGranted = true
    }
}
```

### Race 3: Applier Reading Log vs AppendEntries Truncating

**Scenario:** Applier applies log[5] while AppendEntries truncates log to length 4

**Without locks:**
```
Applier:                       AppendEntries:
entry := rf.log[5]             rf.log = rf.log[:4]  // Truncate!
        ^                              ^
        |______ PANIC! Index out of bounds! ______|
```

**Consequence:** Applier crashes trying to access non-existent entry

**Solution (Lines 976-995):**
```go
// Applier goroutine
for !rf.killed() {
    rf.mu.Lock()
    for rf.lastApplied < rf.commitIndex {
        rf.lastApplied++
        entry := rf.log[rf.logIndexToSlice(rf.lastApplied)]
        msg := ApplyMsg{...}
        
        rf.mu.Unlock()  // ← CRITICAL: Release BEFORE channel send!
        rf.applyCh <- msg
        rf.mu.Lock()    // ← Re-acquire after send
    }
    rf.mu.Unlock()
}
```

### Race 4: Leader Replication Loop (Concurrent RPC Replies)

**Scenario:** Two AppendEntries replies arrive simultaneously, both update `nextIndex[i]`

**Without locks:**
```
Reply 1 (from server 2):       Reply 2 (from server 2):
rf.nextIndex[2] = 50           rf.nextIndex[2] = 45
```

**Consequence:** Lost update—one write overwrites the other

**Solution (Lines 886-897):**
```go
go func(server int) {
    // ... send RPC ...
    
    rf.mu.Lock()
    // Check stale response FIRST
    if rf.state != Leader || rf.currentTerm != currentTerm {
        rf.mu.Unlock()
        return
    }
    // Now safe to update
    rf.nextIndex[server] = ...
    rf.matchIndex[server] = ...
    rf.mu.Unlock()
}(i)
```

---

## The Deadly Deadlock: applyCh Pattern

### The Problem

**Deadlock Scenario:**
1. Applier holds `rf.mu` lock
2. Applier tries to send to `rf.applyCh <- msg` (blocks waiting for receiver)
3. Service tries to call `rf.Start()` to handle command
4. `Start()` needs `rf.mu` lock (blocks waiting for applier)
5. **Circular wait:** Applier waits for service, service waits for applier → DEADLOCK

**Code that would deadlock:**
```go
// ❌ WRONG - Holds lock during channel send
rf.mu.Lock()
for rf.lastApplied < rf.commitIndex {
    rf.lastApplied++
    entry := rf.log[rf.lastApplied]
    msg := ApplyMsg{...}
    rf.applyCh <- msg  // ← BLOCKS while holding lock!
}
rf.mu.Unlock()
```

### The Solution (Lines 976-995)

```go
// ✅ CORRECT - Release lock before blocking operation
for !rf.killed() {
    rf.mu.Lock()
    for rf.lastApplied < rf.commitIndex {
        rf.lastApplied++
        entry := rf.log[rf.logIndexToSlice(rf.lastApplied)]
        msg := ApplyMsg{
            CommandValid: true,
            Command:      entry.Command,
            CommandIndex: rf.lastApplied,
        }
        rf.mu.Unlock()       // ← Release lock BEFORE send
        rf.applyCh <- msg    // ← Can block safely
        rf.mu.Lock()         // ← Re-acquire after send
    }
    rf.mu.Unlock()
    time.Sleep(10 * time.Millisecond)
}
```

**Why this works:**
- Lock is released while waiting for channel receiver
- Service can call `Start()` and acquire lock
- No circular dependency

---

## Stale RPC Response Pattern

### The Problem

**Scenario:**
1. Server is Leader in term 5, sends AppendEntries to follower
2. Server steps down to Follower in term 6 (received higher term)
3. AppendEntries reply arrives (from term 5)
4. Should server process this reply?

**Answer:** NO! The reply is stale—we're no longer the leader who sent it.

### The Solution: State/Term Validation (Lines 787-790)

```go
go func(server int) {
    rf.mu.Lock()
    // Capture state BEFORE sending RPC
    currentTerm := rf.currentTerm
    rf.mu.Unlock()
    
    // Send RPC (lock released)
    ok := rf.sendAppendEntries(server, &args, &reply)
    
    if ok {
        rf.mu.Lock()
        // CRITICAL: Validate state hasn't changed
        if rf.state != Leader || rf.currentTerm != currentTerm {
            rf.mu.Unlock()
            return  // Ignore stale response
        }
        
        // Safe to process reply
        rf.nextIndex[server] = ...
        rf.mu.Unlock()
    }
}(i)
```

**Why check both `state` and `currentTerm`?**
- `state`: We might have stepped down to Follower
- `currentTerm`: We might have started new term as leader

---

## Concurrency Quiz Exercises

### Exercise 4.1: Race Detection

**Scenario:** You remove the mutex from the ticker goroutine:
```go
// BUGGY: No lock!
func (rf *Raft) ticker() {
    for !rf.killed() {
        time.Sleep(50 * time.Millisecond)
        timeSince := time.Since(rf.lastHeartbeat)  // ❌ No lock!
        if timeSince > rf.electionTimeout {
            // Start election
            rf.currentTerm++  // ❌ No lock!
            rf.votedFor = rf.me
            rf.state = Candidate
        }
    }
}
```

**Questions:**
1. What race occurs between ticker and AppendEntries handler?
2. What could happen if ticker increments `currentTerm` while AppendEntries reads it?
3. Would `go test -race` catch this?
4. What is worst-case consequence?

**Answers:**
1. Both access `lastHeartbeat`, `currentTerm`, `state` without synchronization
2. AppendEntries might read partially-written term, or ticker might overwrite AppendEntries' term update
3. Yes! `-race` detects unsynchronized access to shared memory
4. Safety violation: corrupted state leading to split-brain or data loss

---

### Exercise 4.2: Deadlock Scenario

**Scenario:** Applier doesn't release lock before channel send:
```go
// BUGGY
rf.mu.Lock()
for rf.lastApplied < rf.commitIndex {
    rf.lastApplied++
    entry := rf.log[rf.lastApplied]
    msg := ApplyMsg{...}
    rf.applyCh <- msg  // ❌ Holds lock during send!
}
rf.mu.Unlock()
```

**Service code:**
```go
for msg := range applyCh {
    // Process message
    if isReadCommand(msg) {
        result := rf.Start(readOp)  // Needs rf.mu lock!
    }
}
```

**Questions:**
1. Draw the dependency graph showing the deadlock.
2. At what point does the system freeze?
3. How would you detect this? (Tool or symptom)
4. Why doesn't `defer rf.mu.Unlock()` help here?

**Answers:**
1. Applier holds `rf.mu`, waits for service to read channel. Service waits for `rf.mu` to call Start(). Circular!
2. Freezes when applier sends to full/unbuffered channel and service tries to call Start()
3. Tests timeout (no progress), or run with `GODEBUG=gctrace=1` to see goroutines blocked
4. `defer` only helps with early returns, not with blocking operations while lock held

---

### Exercise 4.3: Stale RPC Processing

**Scenario:**
- T=0: Server A is Leader in term 5, sends AppendEntries to B
- T=50: Server C wins election for term 6, sends heartbeat to A
- T=60: A steps down to Follower, term 6
- T=70: Reply from B arrives (success, from term 5 RPC)

**Code processes reply without checking:**
```go
// BUGGY
ok := rf.sendAppendEntries(server, &args, &reply)
if ok && reply.Success {
    rf.mu.Lock()
    rf.matchIndex[server] = ...  // ❌ Update state as Follower!
    rf.mu.Unlock()
}
```

**Questions:**
1. What state is A in when processing reply at T=70?
2. Should A update `matchIndex[server]`?
3. What could break if A updates `matchIndex` as Follower?
4. What check prevents this?

**Answers:**
1. A is Follower in term 6
2. No! A is no longer the leader who sent this RPC
3. Could corrupt state (followers don't maintain matchIndex), or next leader initialization could use wrong values
4. Check `if rf.state != Leader || rf.currentTerm != currentTerm` before processing (Line 787)

---

## Lock-Free Operations (Safe Without Mutex)

**These operations don't need locks:**

1. **Reading `rf.killed()`** - Uses atomic operations (Line 657-659)
2. **Calling RPC send functions** - Network I/O, lock released before call
3. **Reading immutable fields** - `rf.me`, `rf.peers` (set once in Make(), never changed)
4. **Local variables in goroutine** - Each goroutine has own stack

**Everything else needs `rf.mu` protection!**

---

## Summary: Concurrency Patterns Checklist

| Pattern | Location | Purpose | What Breaks Without It |
|---------|----------|---------|----------------------|
| Lock all shared access | All functions | Prevent data races | Corrupted state, crashes |
| `defer rf.mu.Unlock()` | All functions | Ensure unlock on early return | Deadlock (lock never released) |
| Release lock before channel | Line 985 | Prevent deadlock | Circular wait with service |
| Stale RPC check | Line 787 | Ignore old responses | Process replies for wrong role/term |
| Capture term before RPC | Line 773 | Detect term changes | Can't identify stale replies |

---


# PART 5: Quiz-Style Reasoning Exercises

## How to Use This Section

Each exercise follows the quiz format: **Scenario → Question → Reasoning → Answer**

**Study approach:**
1. Read scenario, cover the answer
2. Reason through step-by-step (use Figure 2!)
3. Check your reasoning against answer
4. If wrong, understand WHY, not just memorize answer

---

## Safety Violation Exercises

### Exercise 5.1: The Figure 8 Scenario

**Scenario Setup:**
```
S1: [e1(T1), e2(T2)]  ← Leader in T2, crashes before committing e2
S2: [e1(T1), e2(T2)]  ← Has e2 from S1
S3: [e1(T1), e3(T3)]  ← Was leader in T3, committed e3, now follower

Network partition heals. S1 becomes leader in T4.
S1 sees majority (S1, S2) has e2(T2). Can S1 commit e2 by counting replicas?
```

**Question:** What breaks if S1 commits e2(T2) in term T4 by counting?

**Reasoning:**
1. S1 counts: S1 has e2, S2 has e2 → majority → commit e2? 
2. But wait: S3 doesn't have e2, it has e3(T3)
3. If S3 becomes leader in T5 (e3 has higher term than e2), S3 will overwrite e2
4. But e2 was "committed" according to S1! 
5. **Committed entry overwritten** → SAFETY VIOLATION

**Answer:** YES, this breaks safety. S1 might commit e2, then S3 becomes leader and overwrites it.

**Figure 2 Rule:** "Raft never commits log entries from previous terms by counting replicas; only log entries from the leader's current term are committed by counting replicas"

**Code Prevention (Line 740):**
```go
if rf.getTermAtIndex(N) != rf.currentTerm {
    continue  // Skip old-term entries
}
```

**How to actually commit e2:** S1 must append a new entry e4(T4), then commit both e4 and e2 together when e4 gets majority.

---

### Exercise 5.2: Double-Voting After Crash

**Scenario:**
```
T=0: Server S1 in term 5, votes for Candidate C1 (votedFor=2)
T=1: S1 calls persist() to save votedFor=2
T=2: S1 crashes before persist() completes (data not written to disk)
T=3: S1 restarts, calls readPersist()
T=4: Candidate C2 sends RequestVote for term 5
```

**Question:** Will S1 grant vote to C2? Is this a problem?

**Reasoning:**
1. After restart, readPersist() loads persistent state
2. If persist() didn't complete, votedFor might still be -1 (not saved)
3. S1 sees votedFor=-1, term=5, grants vote to C2
4. But S1 already voted for C1 in term 5!
5. **Double-voting** → two leaders in term 5

**Answer:** If persist() didn't complete, S1 might double-vote. This violates safety.

**Prevention:** persist() must complete BEFORE replying to RPC (Line 305)

```go
func (rf *Raft) RequestVote(args, reply) {
    rf.mu.Lock()
    defer rf.mu.Unlock()
    
    // ... check conditions ...
    
    rf.votedFor = args.CandidateId
    rf.persist()  // ← MUST complete before setting reply
    reply.VoteGranted = true
}
```

**Critical timing:** Persist BEFORE reply, not after! Reply reaching candidate = commitment.

---

### Exercise 5.3: Accepting Stale Leader

**Scenario:**
```
Server S1 state: term=8, log=[e1, e2, e3]
S1 crashes, loses volatile state (commitIndex, lastApplied, etc.)

Stale leader L (still in term 6) sends AppendEntries:
- term=6
- entries=[e4]

S1 restarts with term=8 (persisted).
```

**Question:** Should S1 accept AppendEntries from L? What prevents this?

**Reasoning:**
1. S1 receives AppendEntries with args.Term=6
2. S1's currentTerm=8 (restored from persist)
3. Check: args.Term (6) < rf.currentTerm (8)?
4. YES → reject RPC (Line 377)

**Answer:** No, S1 rejects. Term comparison prevents accepting stale leaders.

```go
if args.Term < rf.currentTerm {
    reply.Term = rf.currentTerm
    reply.Success = false
    return
}
```

**Why this matters:** Without persistence of currentTerm, S1 would restart with term=0, accept term 6 leader, overwrite entries → SAFETY VIOLATION

---

## Liveness Violation Exercises

### Exercise 5.4: Split Vote Hell

**Scenario:**
```
5 servers, all have same election timeout: 200ms
Leader crashes at T=0
All servers timeout simultaneously at T=200ms
All become candidates, all vote for themselves
Vote distribution: S1:1, S2:1, S3:1, S4:1, S5:1 (no majority)
```

**Question:** Will a leader ever be elected? How does RAFT break this cycle?

**Reasoning:**
1. No server gets majority (need 3 votes, each has 1)
2. All servers timeout again... when?
3. If timeouts are same (200ms), they all timeout at T=400ms again!
4. Repeat split vote forever → NO LEADER → NO PROGRESS

**Answer:** Without randomization, system is stuck. RAFT uses randomized timeouts (150-300ms).

**How randomization helps:**
- S1 times out at 180ms → starts election T=180
- S2 times out at 250ms → votes for S1
- S3 times out at 200ms → votes for S1
- S1 gets majority before others start elections

**Code (Line 668):**
```go
rf.electionTimeout = time.Duration(150+rand.Intn(150)) * time.Millisecond
```

**Why 150-300ms range?** Low enough for 5-second requirement, high enough to avoid heartbeat (100ms) triggering elections.

---

### Exercise 5.5: Constant Unnecessary Elections

**Scenario:**
```
Leader L sends heartbeats every 100ms
Follower F has election timeout 200ms
F receives heartbeat at T=0, T=100, T=200, ...

BUT: F forgets to reset election timer when receiving heartbeats
```

**Question:** What happens to F? Does this affect the cluster?

**Reasoning:**
1. F receives heartbeat at T=0
2. F processes AppendEntries... but doesn't call resetElectionTimer()
3. At T=200, F's timer expires (200ms since... when?)
4. If timer is never reset, it's been 200ms since F became Follower initially
5. F starts election, increments term, sends RequestVote
6. Leader receives RequestVote with higher term, steps down
7. Cluster must elect new leader (probably F)
8. Repeat every ~200ms → CONSTANT ELECTIONS

**Answer:** F triggers new election every timeout period despite healthy leader. Wastes resources, prevents progress.

**Prevention (Line 400):**
```go
func (rf *Raft) AppendEntries(args, reply) {
    rf.mu.Lock()
    defer rf.mu.Unlock()
    
    // ... process RPC ...
    
    rf.resetElectionTimer()  // ← CRITICAL for liveness
}
```

**Also reset on granting vote (Line 304):** Prevents voted server from competing with candidate it just voted for.

---

### Exercise 5.6: The Deadlock Trap

**Scenario:**
```
Applier goroutine code:
    rf.mu.Lock()
    msg := ApplyMsg{Command: rf.log[rf.lastApplied].Command, ...}
    rf.applyCh <- msg  // ← Blocks waiting for service to read
    rf.mu.Unlock()

Service code:
    for msg := range applyCh {
        result := processCommand(msg.Command)
        rf.Start(result)  // ← Needs rf.mu lock!
    }
```

**Question:** System freezes. Draw the deadlock cycle.

**Reasoning:**
1. Applier acquires `rf.mu`
2. Applier tries to send to `applyCh` (blocks—channel is unbuffered or full)
3. Service is blocked reading from `applyCh` (waits for applier to send)
4. Service receives message, calls `rf.Start()`
5. `Start()` needs `rf.mu` lock (waits for applier to release)
6. **Cycle:** Applier waits for service → service waits for applier

**Deadlock graph:**
```
Applier: holds(rf.mu) → waits(applyCh receiver)
              ↓                    ↑
Service: waits(rf.mu) ← holds(applyCh receiver)
```

**Answer:** Classic deadlock. Applier holds lock and waits, service waits for lock.

**Fix (Line 985):**
```go
rf.mu.Unlock()       // Release BEFORE blocking operation
rf.applyCh <- msg    // Can block safely now
rf.mu.Lock()         // Re-acquire after
```

**General rule:** Never hold mutex while performing blocking operation (channel send/receive, network I/O, waiting).

---

## Edge Case Exercises

### Exercise 5.7: InstallSnapshot vs AppendEntries Race

**Scenario:**
```
Leader L state: lastIncludedIndex=100, log=[base@100, e101, e102]
Follower F state: lastIncludedIndex=0, log=[dummy, e1, e2, ..., e95]

L determines F is behind (nextIndex[F]=96 < lastIncludedIndex=100)
L sends InstallSnapshot to F... takes 500ms to arrive

Meanwhile, L's heartbeat loop sends AppendEntries to F (different goroutine)
AppendEntries arrives first!
```

**Question:** What does F do with AppendEntries(prevLogIndex=100, ...)?

**Reasoning:**
1. F receives AppendEntries with prevLogIndex=100
2. F checks: does F have entry at index 100?
3. F's lastLogIndex()=95 < 100 → NO
4. F replies Success=false, XLen=96 (log too short)
5. L receives failure, decrements nextIndex[F]... but also sent InstallSnapshot!
6. InstallSnapshot arrives at F
7. F installs snapshot, now lastIncludedIndex=100
8. L sends next AppendEntries with prevLogIndex=100 → now F has it (base entry)!

**Answer:** F rejects first AppendEntries (log too short), then installs snapshot, then accepts subsequent AppendEntries. No problem—system is designed for this race.

**Code handling (Line 406):**
```go
// In AppendEntries handler
if args.PrevLogIndex < rf.lastIncludedIndex {
    // Leader is behind, tell it to catch up
    reply.XLen = rf.lastIncludedIndex
    return
}
```

---

### Exercise 5.8: Snapshot Immediately After Start()

**Scenario:**
```
Leader state: lastIncludedIndex=50, log=[base@50, e51, e52]
Client calls Start(cmd) → returns index=53
Service immediately calls Snapshot(53) on leader

Question: What happens to the log? Can client still see commit?
```

**Reasoning:**
1. Start() appends e53, log=[base@50, e51, e52, e53], returns index=53
2. Snapshot(53) trims log through index 53
3. New log=[base@53, ...] (entries ≤53 discarded)
4. Entry e53 is now in snapshot, not in log
5. When e53 commits, applier should send snapshot to service... but service CALLED Snapshot!
6. Circular dependency? No—service has already processed e53 locally, that's WHY it's snapshotting

**Answer:** Log becomes [base@53]. Service doesn't wait for ApplyMsg for index 53 because service already applied it locally (that's the point of snapshotting). Client sees commit through service's own state, not through RAFT.

**Code (Line 207):**
```go
if index <= rf.lastIncludedIndex {
    return  // Already snapshotted, ignore
}
```

**Key insight:** Service calls Snapshot() AFTER applying entry to state machine. It's telling RAFT "I have this, you can discard."

---

### Exercise 5.9: Fast Backup Without Conflict Term

**Scenario:**
```
Leader L: lastIncludedIndex=50, log=[base@50, e51(T5), e52(T5), e53(T6), e54(T6)]
Follower F: lastIncludedIndex=50, log=[base@50, e51(T5)]
L sends: AppendEntries(prevLogIndex=53, prevLogTerm=6, entries=[e54])

F check: Does F have entry at index 53? No (log too short)
F reply: Success=false, XTerm=-1, XLen=52, XIndex=-1
```

**Question:** How does L use this reply to update nextIndex[F]?

**Reasoning:**
1. L receives reply with XTerm=-1 (means log too short, not conflict)
2. L checks fast backup code (Line 860-875)
3. If XTerm=-1: `nextIndex[F] = reply.XLen = 52`
4. L's next AppendEntries will use prevLogIndex=51 (52-1)
5. F has entry 51 → will match → success!

**Answer:** L sets nextIndex[F]=52, jumps directly to F's log end. Much faster than decrementing nextIndex one by one.

**Without optimization:** nextIndex goes 53→52→... requires 2 RPCs
**With optimization:** nextIndex jumps 53→52 in 1 RPC

**Code (Line 860-863):**
```go
if reply.XTerm == -1 {
    // Log too short
    rf.nextIndex[server] = reply.XLen
}
```

---

### Exercise 5.10: Leader Election with Outdated Log

**Scenario:**
```
S1: [e1(T1), e2(T1), e3(T2), e4(T2), e5(T2)]  ← Follower, most up-to-date
S2: [e1(T1), e2(T1), e3(T2)]                   ← Candidate in T3
S3: [e1(T1), e2(T1)]                           ← Follower

Leader crashes. S2 starts election (term 3), sends RequestVote to S1 and S3.
```

**Question:** Will S1 grant vote to S2? Will S2 become leader?

**Reasoning (at S1):**
1. S2 sends RequestVote(term=3, lastLogIndex=3, lastLogTerm=2)
2. S1 checks election restriction (Line 290-297):
   - S2's lastLogTerm=2, S1's lastLogTerm=2 → same
   - S2's lastLogIndex=3, S1's lastLogIndex=5 → S2 shorter!
   - logIsUpToDate = false
3. S1 rejects vote (Line 308)
4. S3 might grant vote, but S2 needs majority (2 out of 3)
5. S2 gets only 1 vote (self) → NOT elected

**Answer:** No, S1 rejects because S2's log is not up-to-date. S2 doesn't become leader.

**What happens next:**
- S1 times out, becomes candidate in term 4
- S1 sends RequestVote with lastLogIndex=5, lastLogTerm=2
- Both S2 and S3 grant votes (S1's log is up-to-date)
- S1 elected → has all committed entries → SAFETY preserved

**Code (Line 293-297):**
```go
logIsUpToDate := false
if args.LastLogTerm > lastLogTerm {
    logIsUpToDate = true
} else if args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex {
    logIsUpToDate = true
}
```

**Why this prevents data loss:** New leader MUST have all committed entries. Election restriction ensures this.

---

## Summary: Reasoning Patterns

| Scenario Type | Key Question | Figure 2 Rule | Code Check |
|---------------|--------------|---------------|------------|
| Can commit old term entry? | Safety violation? | Only commit currentTerm | Line 740 term check |
| Forgot to persist before reply? | Double-vote after crash? | Persistent state timing | Line 305, 391 persist() |
| Split votes | Will leader be elected? | Randomized timeouts | Line 668 rand.Intn(150) |
| Forgot timer reset | Unnecessary elections? | Reset on valid RPC | Line 304, 400 |
| Hold lock during channel send | Deadlock? | Release before blocking | Line 985 unlock before send |
| Log too short / outdated | Election restriction | Up-to-date check | Line 293-297 |
| Fast backup | Optimize nextIndex update | XTerm/XIndex/XLen | Line 860-875 |

---


# PART 6: Test Coverage Map

## Why This Section Matters

**Each test validates a specific RAFT property.** Understanding what each test checks helps you:
1. Debug test failures (know what property is violated)
2. Anticipate quiz questions (tests = common failure scenarios)
3. Verify your implementation is complete

---

## Phase 3A: Leader Election (3 tests)

### TestInitialElection3A
**What it tests:** Basic leader election in absence of failures
**Success criteria:**
- One leader elected within 5 seconds
- Leader maintains leadership (no unnecessary elections)
- Leader sends heartbeats to maintain authority

**Key implementation:**
- Ticker starts election on timeout (Line 1007-1030)
- RequestVote RPC works (Lines 266-312)
- AppendEntries heartbeats work (Lines 760-920)
- Election restriction prevents outdated candidates (Line 293-297)

**If this fails:** Check election timer logic, RequestVote implementation

---

### TestReElection3A
**What it tests:** New leader elected after old leader fails
**Scenario:**
- Disconnect old leader
- New leader should be elected within 5 seconds
- Reconnect old leader → should recognize new leader, step down

**Key implementation:**
- Followers detect leader failure (election timeout)
- New election starts, majority votes collected
- Old leader accepts higher term, converts to follower (Line 387-391)

**If this fails:** Check leader failure detection, term comparison logic

---

### TestManyElections3A
**What it tests:** System handles multiple elections reliably
**Scenario:**
- 7 servers (larger cluster)
- Network partitions, reconnections, multiple leader changes
- Eventually one stable leader

**Key implementation:**
- Randomized election timeouts break split votes (Line 668)
- Servers handle term updates correctly
- Multiple concurrent elections don't break state

**If this fails:** Check randomization, election timeout stability

---

## Phase 3B: Log Replication (10 tests)

### TestBasicAgree3B
**What it tests:** Commands replicate to followers and commit
**Scenario:**
- Submit command to leader via Start()
- Leader replicates to followers
- Command commits, appears on applyCh

**Key implementation:**
- Start() appends to log (Line 619-640)
- AppendEntries sends entries (Line 821-856)
- advanceCommitIndex() detects majority (Line 723-758)
- Applier sends to applyCh (Line 960-995)

**If this fails:** Check Start(), AppendEntries replication, commit logic

---

### TestRPCBytes3B
**What it tests:** Not too many RPC bytes sent
**Purpose:** Ensure efficiency (don't send huge RPCs unnecessarily)

**Key implementation:**
- AppendEntries sends only new entries, not entire log
- Heartbeats send empty entries array

**If this fails:** Check you're not sending full log every heartbeat

---

### TestFollowerFailure3B
**What it tests:** System continues with follower failures
**Scenario:**
- One follower disconnected
- Leader + remaining majority continue committing
- Disconnected follower reconnects → catches up

**Key implementation:**
- Leader doesn't wait for all followers (majority sufficient)
- nextIndex[] tracks each follower independently
- Catch-up works when follower reconnects

**If this fails:** Check majority logic, don't require all servers

---

### TestLeaderFailure3B
**What it tests:** System recovers from leader failure
**Scenario:**
- Leader fails mid-replication
- New leader elected
- New leader continues log replication
- Old leader reconnects → becomes follower, log updated

**Key implementation:**
- Election restriction ensures new leader has committed entries
- New leader reinitializes nextIndex[], matchIndex[]
- Old leader accepts new leader's log (consistency check)

**If this fails:** Check election restriction, leader initialization

---

### TestFailAgree3B
**What it tests:** Agreement despite follower failures
**Scenario:**
- Submit commands
- Disconnect random followers
- Commands still commit (if majority available)
- Reconnected followers catch up

**Key implementation:**
- Log consistency check (prevLogIndex/prevLogTerm)
- Log repair when follower behind (AppendEntries with missing entries)
- nextIndex[] decrement on failure

**If this fails:** Check consistency check, log repair logic

---

### TestFailNoAgree3B
**What it tests:** No progress without majority
**Scenario:**
- Disconnect majority of servers
- Submit command → should NOT commit
- Reconnect → command commits

**Key implementation:**
- advanceCommitIndex() requires majority (count > len/2)
- Leader doesn't unilaterally commit

**If this fails:** Check majority count logic in advanceCommitIndex()

---

### TestConcurrentStarts3B
**What it tests:** Multiple concurrent Start() calls work correctly
**Scenario:**
- Multiple clients submit commands simultaneously
- All commands replicate and commit
- Correct order maintained

**Key implementation:**
- Start() is thread-safe (mutex protection)
- Each command gets unique index
- Commands commit in log order

**If this fails:** Check Start() concurrency, index allocation

---

### TestRejoin3B
**What it tests:** Server rejoining after partition
**Scenario:**
- Server partitioned, misses many commits
- Server rejoins
- Server's log repaired to match leader

**Key implementation:**
- Consistency check detects divergence
- Leader backs up nextIndex[] to find common point
- Log overwritten from divergence point

**If this fails:** Check consistency check, log truncation logic

---

### TestBackup3B
**What it tests:** Log repair efficiency with large gaps
**Scenario:**
- Follower far behind (missing many entries)
- Should catch up quickly (not one RPC per entry)

**Key implementation:**
- Fast backup optimization (XTerm/XIndex/XLen)
- Leader jumps to first diverging term, not single-step

**If this fails:** Implement fast backup (Lines 365-367, 860-875)

**Performance impact:** Without optimization, test takes ~33s. With optimization, ~26s.

---

### TestCount3B
**What it tests:** RPC rate limits
**Success criteria:**
- Leader sends ≤10 heartbeats/second
- Reasonable total RPC count

**Key implementation:**
- Heartbeat loop sleeps 100ms between iterations (Line 927)
- Don't send unnecessary AppendEntries

**If this fails:** Check heartbeat frequency, add sleep if needed

---

## Phase 3C: Persistence (8 tests)

### TestPersist13C
**What it tests:** Basic crash recovery
**Scenario:**
- Servers agree on commands
- One server crashes and restarts
- Restarted server has correct state

**Key implementation:**
- persist() called after state changes
- readPersist() restores state on startup
- currentTerm, votedFor, log[] all persisted

**If this fails:** Check persist() call sites, encoding/decoding

---

### TestPersist23C
**What it tests:** Multiple crashes and restarts
**Scenario:**
- Submit commands
- Crash different servers at different times
- All servers eventually have same log

**Key implementation:**
- Persistence works across multiple crashes
- Restored servers participate correctly
- Log repair works with persisted state

**If this fails:** Check readPersist() logic, persist() completeness

---

### TestPersist33C
**What it tests:** Partition with crashes
**Scenario:**
- Network partition
- Servers crash in each partition
- Partition heals → correct log convergence

**Key implementation:**
- Persistence + log repair together
- Term comparison after restart
- Correct leader election after partition

**If this fails:** Check interaction between persistence and network issues

---

### TestFigure83C ⭐ CRITICAL TEST
**What it tests:** THE FIGURE 8 SCENARIO (only commit currentTerm entries)
**Scenario:**
- Leader in term 2 replicates entry to majority, crashes before committing
- New leader in term 3 with different log elected
- Original leader rejoins in term 4
- Sees majority has old entry → CAN'T commit by counting!

**Key implementation:**
- advanceCommitIndex() checks `getTermAtIndex(N) == currentTerm` (Line 740)
- Only commit entries from current term

**If this fails:** You're committing old-term entries → SAFETY VIOLATION

**This test validates the core safety property!**

---

### TestUnreliableAgree3C
**What it tests:** Agreement despite unreliable network
**Scenario:**
- Network drops/delays/reorders RPCs randomly
- Commands still commit correctly
- Persistence works despite RPC failures

**Key implementation:**
- Retry logic (implicit in heartbeat loop)
- Idempotent RPC handling
- Persistence timing (before reply)

**If this fails:** Check RPC handling, ensure correctness despite losses

---

### TestFigure8Unreliable3C
**What it tests:** Figure 8 scenario + unreliable network
**Combines:** Figure 8 safety + network unreliability

**If this fails:** Check both currentTerm commit rule AND network handling

---

### TestReliableChurn3C
**What it tests:** Continuous leader changes, reliable network
**Scenario:**
- Disconnect/reconnect leader repeatedly
- Submit commands throughout
- All committed commands preserved

**Key implementation:**
- Persistence across many leader changes
- Log repair works repeatedly
- No committed entries lost

**If this fails:** Check persistence durability, log repair robustness

---

### TestUnreliableChurn3C
**What it tests:** Leader churn + unreliable network (hardest test!)
**Combines:** All previous challenges

**If this fails:** Debug simpler tests first, then tackle this

---

## Phase 3D: Log Compaction (8 tests)

### TestSnapshotBasic3D
**What it tests:** Basic snapshot creation and log trimming
**Scenario:**
- Service calls Snapshot(index)
- Log trimmed correctly
- New commands still work

**Key implementation:**
- Snapshot() trims log (Lines 202-245)
- Rebuild log with base entry
- lastIncludedIndex tracking

**If this fails:** Check Snapshot() implementation, log rebuild

---

### TestSnapshotInstall3D
**What it tests:** InstallSnapshot RPC works
**Scenario:**
- Leader snapshotted, follower behind snapshot point
- Leader sends InstallSnapshot to follower
- Follower installs, catches up

**Key implementation:**
- Leader detects nextIndex < lastIncludedIndex (Line 793)
- InstallSnapshot RPC structures (Lines 500-590)
- Follower installs snapshot, rebuilds log

**If this fails:** Check InstallSnapshot RPC, follower installation logic

---

### TestSnapshotInstallUnreliable3D
**What it tests:** InstallSnapshot despite unreliable network
**Scenario:**
- Network drops InstallSnapshot RPCs
- Eventually succeeds via retry

**Key implementation:**
- InstallSnapshot in heartbeat loop (retries automatically)
- Idempotent installation (check lastIncludedIndex)

**If this fails:** Check retry logic, idempotence

---

### TestSnapshotInstallCrash3D
**What it tests:** InstallSnapshot + crashes
**Scenario:**
- Follower crashes while installing snapshot
- Restarts, re-installs snapshot

**Key implementation:**
- Snapshot persisted correctly
- readPersist() restores snapshot state
- Re-installation works

**If this fails:** Check snapshot persistence (Lines 224-232)

---

### TestSnapshotInstallUnCrash3D
**What it tests:** Multiple crashes during snapshot installation

**If this fails:** Check persistence + installation interaction

---

### TestSnapshotAllCrash3D
**What it tests:** All servers crash after snapshotting
**Scenario:**
- All servers snapshot
- All crash and restart
- Log rebuilt from snapshots

**Key implementation:**
- Snapshot restoration on startup
- Log continuity after restart

**If this fails:** Check readPersist() snapshot loading

---

### TestSnapshotInit3D
**What it tests:** Snapshot on initialization (service has existing snapshot)

**If this fails:** Check Make() snapshot initialization (Line 1113-1120)

---

## Test Debugging Strategy

**If test fails:**
1. **Read test code** in raft_test.go (find test name, understand what it's checking)
2. **Check Figure 2 rule** related to test (see Part 2 mapping)
3. **Enable debug logging:** `export VERBOSE=1; go test -run TestName`
4. **Run with race detector:** `go test -race -run TestName`
5. **Run multiple times:** `for i in {0..10}; do go test -run TestName; done`
6. **Simplify:** Comment out parts of implementation to isolate issue

---

## Test Execution Checklist

Before submitting:
- ✅ All 3A tests pass (3/3)
- ✅ All 3B tests pass (10/10)
- ✅ All 3C tests pass (8/8) - ESPECIALLY TestFigure83C!
- ✅ All 3D tests pass (8/8)
- ✅ All tests pass without `-race` flag (0 data races)
- ✅ All tests pass WITH `-race` flag (race detector enabled)
- ✅ Total test time <600 seconds
- ✅ Each individual test <120 seconds
- ✅ Repeated runs pass (run 10 times, all pass)

---

# PART 7: Quick Reference Cheat Sheet

## For Exam Day: One-Page Summary

### Critical Figure 2 Rules → Code Lines

| Rule | Code | Breaks If Violated |
|------|------|-------------------|
| One vote per term | L387: args.Term **>** (not >=) | Double-voting |
| Election restriction | L293-297: log up-to-date check | Leader missing committed entries |
| Only commit currentTerm | L740: term == currentTerm | Figure 8 safety violation |
| Persist before reply | L305, L391: persist() before reply.X = true | State loss after crash |
| Reset timer on RPC | L304, L400: resetElectionTimer() | Unnecessary elections |
| Majority for commit | L753: count > len/2 | Incorrect commits |

---

### Index Translation Formulas (Phase 3D)

```
sliceIndex = logicalIndex - lastIncludedIndex
logicalIndex = lastIncludedIndex + sliceIndex
lastLogIndex() = lastIncludedIndex + len(log) - 1
Start() returns: lastLogIndex() + 1  (LOGICAL, not slice length!)
```

**Always use helpers:** `logIndexToSlice()`, `hasLogEntry()`, `getTermAtIndex()`

---

### The 3 Critical Bugs (Your Secret Weapon)

1. **Double-voting:** Changed `>=` to `>` in AppendEntries (L387)
   - Quiz: "Reset votedFor on same-term heartbeat?" → NO!

2. **Unstable timeout:** Store timeout, don't re-sample (L665-668)
   - Quiz: "Re-sample timeout every loop?" → NO! Moving target

3. **Start() index:** Return logical, not slice length (L621)
   - Quiz: "After Snapshot(50), Start() returns len(log)?" → NO! Wrong index

---

### Concurrency Patterns

```go
// Pattern 1: All shared access
rf.mu.Lock()
defer rf.mu.Unlock()
// ... access rf.field ...

// Pattern 2: Release before blocking
rf.mu.Unlock()
rf.applyCh <- msg  // Blocking operation
rf.mu.Lock()

// Pattern 3: Check stale RPC
if rf.state != Leader || rf.currentTerm != currentTerm {
    return  // Ignore stale response
}
```

---

### Common Quiz Traps Checklist

❌ "Commit old-term entries by counting?" → Figure 8 violation
❌ "Persist AFTER replying to RPC?" → Double-vote after crash
❌ "Reset votedFor on equal-term RPC?" → Double-voting
❌ "Return slice length from Start()?" → Client index mismatch
❌ "Hold lock during applyCh send?" → Deadlock
❌ "Process RPC reply without state check?" → Wrong state actions
❌ "Same election timeout for all?" → Split votes forever
❌ "Don't reset timer on heartbeat?" → Constant elections

---

### Test-to-Concept Map (Most Important)

| Test | Concept | Figure 2 Rule |
|------|---------|---------------|
| TestFigure83C | Only commit currentTerm | Leaders rule 4 |
| TestBackup3B | Fast backup optimization | (Extension) XTerm/XIndex |
| TestReElection3A | Leader failure detection | Followers rule |
| TestFailNoAgree3B | Majority required | Leaders rule 4 count |
| TestSnapshotInstall3D | InstallSnapshot RPC | (Phase 3D) |

---

### Reasoning Framework (Use for ANY quiz question)

**Step 1:** Identify the Figure 2 rule involved
**Step 2:** Trace through state changes step-by-step
**Step 3:** Check for violated invariants (safety/liveness)
**Step 4:** Connect to code location (Part 2 mapping)
**Step 5:** Explain WHY violation is bad (not just WHAT breaks)

---

### Pre-Quiz Final Check (30 minutes before)

1. Review 3 critical bugs (Part 1)
2. Skim Figure 2 mapping table (Part 2)
3. Practice 2-3 index translation problems (Part 3)
4. Read concurrency deadlock example (Part 4, Exercise 4.2)
5. Review Figure 8 scenario (Part 5, Exercise 5.1)
6. Check TestFigure83C description (Part 6)

**Mental model:** Always reason from Figure 2 principles, not memorized code.

---

