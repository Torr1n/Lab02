package raft

// The file raftapi/raft.go defines the interface that raft must
// expose to servers (or the tester), but see comments below for each
// of these functions for more details.
//
// Make() creates a new raft peer that implements the raft interface.

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"cpsc416-2025w1/labgob"
	"cpsc416-2025w1/labrpc"
	"cpsc416-2025w1/raftapi"
	"cpsc416-2025w1/tester1"
)

// ServerState represents the role of a Raft server
type ServerState int

const (
	Follower  ServerState = 0
	Candidate ServerState = 1
	Leader    ServerState = 2
)

// LogEntry represents a single entry in the replicated log
type LogEntry struct {
	Term    int         // Term when entry was received by leader
	Command interface{} // Command for state machine
}

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *tester.Persister   // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	// Your data here (3A, 3B, 3C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	// Persistent state on all servers (Figure 2)
	// Updated on stable storage before responding to RPCs
	currentTerm int        // Latest term server has seen (initialized to 0)
	votedFor    int        // CandidateId that received vote in current term (or -1 if none)
	log         []LogEntry // Log entries; each entry contains command and term

	// Volatile state on all servers (Figure 2)
	commitIndex int // Index of highest log entry known to be committed (initialized to 0)
	lastApplied int // Index of highest log entry applied to state machine (initialized to 0)

	// Volatile state on leaders (Figure 2)
	// Reinitialized after election
	nextIndex  []int // For each server, index of next log entry to send (initialized to leader last log index + 1)
	matchIndex []int // For each server, index of highest log entry known to be replicated (initialized to 0)

	// Snapshot state (Phase 3D)
	// Tracks the last log entry included in the snapshot
	lastIncludedIndex int    // Index of last entry in snapshot
	lastIncludedTerm  int    // Term of last entry in snapshot
	snapshot          []byte // Snapshot data from service
	snapshotPending   bool   // True when a freshly installed snapshot must be delivered to applyCh

	// Custom state for implementation
	state           ServerState   // Current server state (Follower, Candidate, or Leader)
	lastHeartbeat   time.Time     // Time of last heartbeat received (for election timeout)
	electionTimeout time.Duration // Randomized election timeout (stable until reset)
	applyCh         chan raftapi.ApplyMsg // Channel to send committed entries to service
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	term := rf.currentTerm
	isleader := rf.state == Leader
	return term, isleader
}

// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
// before you've implemented snapshots, you should pass nil as the
// second argument to persister.Save().
// after you've implemented snapshots, pass the current snapshot
// (or nil if there's not yet a snapshot).
func (rf *Raft) persist() {
	// Figure 2: Persistent state on all servers
	// Must be updated on stable storage before responding to RPCs
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)

	// Encode persistent state variables (must match decode order)
	// Codex feedback: Check encode errors and panic (fail-fast)
	if err := e.Encode(rf.currentTerm); err != nil {
		panic("Failed to encode currentTerm: " + err.Error())
	}
	if err := e.Encode(rf.votedFor); err != nil {
		panic("Failed to encode votedFor: " + err.Error())
	}
	if err := e.Encode(rf.log); err != nil {
		panic("Failed to encode log: " + err.Error())
	}

	// Phase 3D: Encode snapshot metadata (Codex guidance)
	if err := e.Encode(rf.lastIncludedIndex); err != nil {
		panic("Failed to encode lastIncludedIndex: " + err.Error())
	}
	if err := e.Encode(rf.lastIncludedTerm); err != nil {
		panic("Failed to encode lastIncludedTerm: " + err.Error())
	}

	raftstate := w.Bytes()
	// Phase 3D: Save snapshot alongside raftstate (Codex guidance)
	rf.persister.Save(raftstate, rf.snapshot)
}


// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}

	// Decode persistent state (must match encode order)
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)

	var currentTerm int
	var votedFor int
	var log []LogEntry
	var lastIncludedIndex int
	var lastIncludedTerm int

	// Codex feedback: Check decode errors and panic (fail-fast)
	if err := d.Decode(&currentTerm); err != nil {
		panic("Failed to decode currentTerm: " + err.Error())
	}
	if err := d.Decode(&votedFor); err != nil {
		panic("Failed to decode votedFor: " + err.Error())
	}
	if err := d.Decode(&log); err != nil {
		panic("Failed to decode log: " + err.Error())
	}

	// Phase 3D: Decode snapshot metadata (Codex guidance)
	if err := d.Decode(&lastIncludedIndex); err != nil {
		// No snapshot data - bootstrap case
		lastIncludedIndex = 0
		lastIncludedTerm = 0
	} else {
		if err := d.Decode(&lastIncludedTerm); err != nil {
			panic("Failed to decode lastIncludedTerm: " + err.Error())
		}
	}

	// Restore state
	rf.currentTerm = currentTerm
	rf.votedFor = votedFor
	rf.log = log
	rf.lastIncludedIndex = lastIncludedIndex
	rf.lastIncludedTerm = lastIncludedTerm
	rf.snapshot = rf.persister.ReadSnapshot()

	// Codex feedback: Sanity checks
	// 1. Ensure log has base entry (Codex Phase 4: after snapshot, log[0] = snapshot base, not dummy)
	if len(rf.log) == 0 {
		rf.log = append(rf.log, LogEntry{Term: rf.lastIncludedTerm, Command: nil})
	}

	// 2. Clamp commitIndex and lastApplied (Codex Phase 4: good hygiene for snapshot phase)
	if rf.commitIndex > rf.lastLogIndex() {
		rf.commitIndex = rf.lastLogIndex()
	}
	if rf.lastApplied > rf.lastLogIndex() {
		rf.lastApplied = rf.lastLogIndex()
	}
}

// how many bytes in Raft's persisted log?
func (rf *Raft) PersistBytes() int {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.persister.RaftStateSize()
}


// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Codex Phase 4: Ignore if we already snapshotted past this point
	if index <= rf.lastIncludedIndex {
		return
	}

	// Codex Phase 4: Get term at snapshot point, rebuild log with base entry
	snapshotTerm := rf.getTermAtIndex(index)

	// Trim log: keep entries AFTER index
	// Rebuild with snapshot base entry at position 0
	sliceIndex := rf.logIndexToSlice(index)
	newLog := make([]LogEntry, 0)
	newLog = append(newLog, LogEntry{Term: snapshotTerm, Command: nil}) // Snapshot base
	if sliceIndex+1 < len(rf.log) {
		newLog = append(newLog, rf.log[sliceIndex+1:]...)
	}
	rf.log = newLog

	// Update snapshot metadata
	rf.lastIncludedIndex = index
	rf.lastIncludedTerm = snapshotTerm
	if snapshot != nil {
		snapshotCopy := make([]byte, len(snapshot))
		copy(snapshotCopy, snapshot)
		rf.snapshot = snapshotCopy
	} else {
		rf.snapshot = nil
	}

	// Codex Phase 4: Update commitIndex and lastApplied if they fall below snapshot
	if rf.commitIndex < rf.lastIncludedIndex {
		rf.commitIndex = rf.lastIncludedIndex
	}
	if rf.lastApplied < rf.lastIncludedIndex {
		rf.lastApplied = rf.lastIncludedIndex
	}

	// Persist new state (saves snapshot bytes via persister.Save)
	rf.persist()
}


// RequestVote RPC arguments structure (Figure 2).
// field names must start with capital letters!
type RequestVoteArgs struct {
	Term         int // Candidate's term
	CandidateId  int // Candidate requesting vote
	LastLogIndex int // Index of candidate's last log entry
	LastLogTerm  int // Term of candidate's last log entry
}

// RequestVote RPC reply structure (Figure 2).
// field names must start with capital letters!
type RequestVoteReply struct {
	Term        int  // currentTerm, for candidate to update itself
	VoteGranted bool // true means candidate received vote
}

// RequestVote RPC handler (Figure 2).
// Invoked by candidates to gather votes.
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Reply false if term < currentTerm (Figure 2: RequestVote RPC - Receiver implementation 1)
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
		return
	}

	// If RPC request or response contains term T > currentTerm:
	// set currentTerm = T, convert to follower (Figure 2: Rules for Servers - All Servers)
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist() // Persist state change
	}

	// Get last log index and term for up-to-date check (use helpers)
	lastLogIndex := rf.lastLogIndex()
	lastLogTerm := rf.lastLogTerm()

	// Election restriction: check if candidate's log is at least as up-to-date
	// (Figure 2: RequestVote RPC - Receiver implementation 2)
	logIsUpToDate := false
	if args.LastLogTerm > lastLogTerm {
		logIsUpToDate = true
	} else if args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex {
		logIsUpToDate = true
	}

	// Grant vote if:
	// - votedFor is null (-1) or candidateId
	// - candidate's log is at least as up-to-date as receiver's log
	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && logIsUpToDate {
		rf.votedFor = args.CandidateId
		rf.resetElectionTimer() // Reset election timer when granting vote (Codex feedback)
		rf.persist()            // Persist vote
		reply.VoteGranted = true
	} else {
		reply.VoteGranted = false
	}

	reply.Term = rf.currentTerm
}

// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

// AppendEntries RPC arguments structure (Figure 2).
// field names must start with capital letters!
type AppendEntriesArgs struct {
	Term         int        // Leader's term
	LeaderId     int        // So follower can redirect clients
	PrevLogIndex int        // Index of log entry immediately preceding new ones
	PrevLogTerm  int        // Term of prevLogIndex entry
	Entries      []LogEntry // Log entries to store (empty for heartbeat)
	LeaderCommit int        // Leader's commitIndex
}

// AppendEntries RPC reply structure (Figure 2).
// field names must start with capital letters!
type AppendEntriesReply struct {
	Term    int  // currentTerm, for leader to update itself
	Success bool // true if follower contained entry matching prevLogIndex and prevLogTerm

	// Fast backup optimization (RAFT paper page 8, gray box)
	// Helps leader quickly find the right log index when follower's log diverges
	XTerm  int // Term of the conflicting entry (or -1 if log too short)
	XIndex int // Index of first entry with term XTerm
	XLen   int // Log length (used when log too short)
}

// AppendEntries RPC handler (Figure 2).
// Invoked by leader to replicate log entries; also used as heartbeat.
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Reply false if term < currentTerm (Figure 2: AppendEntries RPC - Receiver implementation 1)
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.Success = false
		return
	}

	// If RPC request contains term T > currentTerm:
	// set currentTerm = T, convert to follower (Figure 2: Rules for Servers - All Servers)
	// CRITICAL: Only reset votedFor for HIGHER terms, not equal terms (Codex feedback)
	// This prevents double-voting within the same term
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist() // Persist state change
	} else if rf.state == Candidate || rf.state == Leader {
		// If we're candidate or leader and receive AppendEntries with equal term,
		// recognize the leader and step down
		rf.state = Follower
	}

	// Reset election timer when receiving valid AppendEntries (Codex feedback)
	// This prevents followers from starting unnecessary elections
	rf.resetElectionTimer()

	// LOG CONSISTENCY CHECK (Figure 2: AppendEntries RPC - Receiver Implementation 2)
	// Reply false if log doesn't contain entry at prevLogIndex whose term matches prevLogTerm

	// Codex Phase 4: Check if prevLogIndex is in snapshot (leader is behind)
	if args.PrevLogIndex < rf.lastIncludedIndex {
		reply.Success = false
		reply.Term = rf.currentTerm
		reply.XTerm = -1
		reply.XLen = rf.lastIncludedIndex
		reply.XIndex = -1
		return
	}

	// Check 1: Log too short (Codex feedback: bounds safety)
	if args.PrevLogIndex > rf.lastLogIndex() {
		reply.Success = false
		reply.Term = rf.currentTerm
		// Fast backup optimization: log too short
		reply.XTerm = -1
		reply.XLen = rf.lastLogIndex() + 1 // Codex Phase 4: logical length
		reply.XIndex = -1
		return
	}

	// Check 2: Term mismatch at prevLogIndex
	// Codex Phase 4: Use getTermAtIndex() for snapshot support
	if args.PrevLogIndex >= 0 {
		prevLogTerm := rf.getTermAtIndex(args.PrevLogIndex)
		if prevLogTerm != args.PrevLogTerm {
			reply.Success = false
			reply.Term = rf.currentTerm
			// Fast backup optimization: conflicting entry
			reply.XTerm = prevLogTerm
			reply.XLen = rf.lastLogIndex() + 1 // Codex Phase 4: logical length
			// Find first index with term XTerm
			reply.XIndex = args.PrevLogIndex
			for reply.XIndex > rf.lastIncludedIndex &&
				rf.getTermAtIndex(reply.XIndex-1) == reply.XTerm {
				reply.XIndex--
			}
			return
		}
	}

	// DELETE CONFLICTING ENTRIES (Figure 2: Receiver Implementation 3)
	// Codex Phase 4: Use logIndexToSlice() and hasLogEntry() for all log access
	// If an existing entry conflicts with a new one (same index but different terms),
	// delete the existing entry and all that follow it
	for i, entry := range args.Entries {
		logicalIndex := args.PrevLogIndex + 1 + i
		if logicalIndex <= rf.lastIncludedIndex {
			continue // Entry already in snapshot
		}
		if rf.hasLogEntry(logicalIndex) {
			sliceIndex := rf.logIndexToSlice(logicalIndex)
			if rf.log[sliceIndex].Term != entry.Term {
				// Conflict detected: truncate log from this point
				rf.log = rf.log[:sliceIndex]
				rf.persist() // Persist truncation
				break
			}
		}
	}

	// APPEND NEW ENTRIES (Figure 2: Receiver Implementation 4)
	// Codex Phase 4: Use hasLogEntry() and skip entries in snapshot
	// Append any new entries not already in the log
	for i, entry := range args.Entries {
		logicalIndex := args.PrevLogIndex + 1 + i
		if logicalIndex <= rf.lastIncludedIndex {
			continue // Entry already in snapshot
		}
		if !rf.hasLogEntry(logicalIndex) {
			// Entry doesn't exist - append it
			rf.log = append(rf.log, entry)
		}
	}

	// Persist if we appended any entries
	if len(args.Entries) > 0 {
		rf.persist()
	}

	// UPDATE COMMIT INDEX (Figure 2: Receiver Implementation 5)
	// If leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry)
	if args.LeaderCommit > rf.commitIndex {
		// Codex feedback: Use min to avoid committing beyond our log
		lastNewIndex := rf.lastLogIndex()
		if args.LeaderCommit < lastNewIndex {
			rf.commitIndex = args.LeaderCommit
		} else {
			rf.commitIndex = lastNewIndex
		}
	}

	reply.Success = true
	reply.Term = rf.currentTerm
}

// sendAppendEntries sends an AppendEntries RPC to a server.
func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

// InstallSnapshot RPC arguments structure (RAFT paper Figure 13).
// field names must start with capital letters!
type InstallSnapshotArgs struct {
	Term              int    // Leader's term
	LeaderId          int    // So follower can redirect clients
	LastIncludedIndex int    // Snapshot replaces all entries up through and including this index
	LastIncludedTerm  int    // Term of lastIncludedIndex
	Data              []byte // Raw bytes of the snapshot
}

// InstallSnapshot RPC reply structure.
// field names must start with capital letters!
type InstallSnapshotReply struct {
	Term int // currentTerm, for leader to update itself
}

// InstallSnapshot RPC handler.
// Invoked by leader to send snapshot to followers that are far behind.
func (rf *Raft) InstallSnapshot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Reply with currentTerm
	reply.Term = rf.currentTerm

	// Reject if term < currentTerm
	if args.Term < rf.currentTerm {
		return
	}

	// Update term if we see higher term
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist()
	}

	// Reset election timer
	rf.resetElectionTimer()

	// Codex Phase 4: Ignore if we already have this snapshot
	if args.LastIncludedIndex <= rf.lastIncludedIndex {
		return
	}

	// Codex Phase 4: Discard log entries covered by snapshot
	// Rebuild log with snapshot base entry
	newLog := make([]LogEntry, 0)
	newLog = append(newLog, LogEntry{Term: args.LastIncludedTerm, Command: nil}) // Snapshot base

	// Keep any entries after the snapshot that we have
	for i := args.LastIncludedIndex + 1; i <= rf.lastLogIndex(); i++ {
		if rf.hasLogEntry(i) {
			sliceIndex := rf.logIndexToSlice(i)
			newLog = append(newLog, rf.log[sliceIndex])
		}
	}

	rf.log = newLog
	rf.lastIncludedIndex = args.LastIncludedIndex
	rf.lastIncludedTerm = args.LastIncludedTerm
	if args.Data != nil {
		snapshotCopy := make([]byte, len(args.Data))
		copy(snapshotCopy, args.Data)
		rf.snapshot = snapshotCopy
	} else {
		rf.snapshot = nil
	}
	rf.snapshotPending = true

	// Codex Phase 4: Update commitIndex and lastApplied
	if rf.commitIndex < rf.lastIncludedIndex {
		rf.commitIndex = rf.lastIncludedIndex
	}
	if rf.lastApplied < rf.lastIncludedIndex {
		rf.lastApplied = rf.lastIncludedIndex
	}

	// Persist new state
	rf.persist()

	// Signal applier to deliver snapshot (applier will detect lastApplied < lastIncludedIndex)
}

// sendInstallSnapshot sends an InstallSnapshot RPC to a server.
func (rf *Raft) sendInstallSnapshot(server int, args *InstallSnapshotArgs, reply *InstallSnapshotReply) bool {
	ok := rf.peers[server].Call("Raft.InstallSnapshot", args, reply)
	return ok
}

// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Check if this server is the leader
	if rf.state != Leader {
		return -1, -1, false
	}

	// Codex Phase 4: Compute LOGICAL index first, BEFORE appending
	// This is the index the client will see when entry is committed
	index := rf.lastLogIndex() + 1
	term := rf.currentTerm

	// Append command to local log with current term (Figure 2: Leaders - Upon receiving command from client)
	entry := LogEntry{
		Term:    term,
		Command: command,
	}
	rf.log = append(rf.log, entry)

	// Codex feedback: Update self bookkeeping immediately
	// Leader knows it has this entry, no need to wait for heartbeat
	rf.matchIndex[rf.me] = index
	rf.nextIndex[rf.me] = index + 1

	// Persist the updated log
	rf.persist()

	// Codex Phase 4: Return LOGICAL index (not slice length!)
	return index, term, true
}

// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

// resetElectionTimer resets the election timer with a new randomized timeout.
// Must be called with rf.mu held.
// Codex feedback: Sample a stable timeout that won't change until next reset.
func (rf *Raft) resetElectionTimer() {
	rf.lastHeartbeat = time.Now()
	// Use 150-300ms range to match Figure 2 (Codex suggestion)
	rf.electionTimeout = time.Duration(150+rand.Intn(150)) * time.Millisecond
}

// logIndexToSlice converts logical log index to slice index.
// Must be called with rf.mu held.
// Codex Phase 4: After snapshots, log[0] represents lastIncludedIndex, not index 0.
func (rf *Raft) logIndexToSlice(logicalIndex int) int {
	return logicalIndex - rf.lastIncludedIndex
}

// hasLogEntry checks if we have the entry at logicalIndex.
// Returns false if entry is in snapshot (index <= lastIncludedIndex) or doesn't exist.
// Must be called with rf.mu held.
func (rf *Raft) hasLogEntry(logicalIndex int) bool {
	if logicalIndex <= rf.lastIncludedIndex {
		return false // Entry is in snapshot
	}
	if logicalIndex > rf.lastLogIndex() {
		return false // Entry doesn't exist yet
	}
	return true
}

// getTermAtIndex returns term at logical index.
// Handles both snapshot base (lastIncludedIndex) and regular log entries.
// Must be called with rf.mu held.
func (rf *Raft) getTermAtIndex(logicalIndex int) int {
	if logicalIndex == rf.lastIncludedIndex {
		return rf.lastIncludedTerm
	}
	if logicalIndex < rf.lastIncludedIndex {
		panic(fmt.Sprintf("Requesting term for compacted entry: %d < %d", logicalIndex, rf.lastIncludedIndex))
	}
	sliceIndex := rf.logIndexToSlice(logicalIndex)
	if sliceIndex < 0 || sliceIndex >= len(rf.log) {
		panic(fmt.Sprintf("Index out of bounds: logical=%d, slice=%d, len=%d", logicalIndex, sliceIndex, len(rf.log)))
	}
	return rf.log[sliceIndex].Term
}

// lastLogIndex returns the LOGICAL index of the last log entry.
// Must be called with rf.mu held.
// Codex Phase 4: Must return logical index, not slice index.
func (rf *Raft) lastLogIndex() int {
	return rf.lastIncludedIndex + len(rf.log) - 1
}

// lastLogTerm returns the term of the last log entry.
// Must be called with rf.mu held.
// Codex Phase 4: Use getTermAtIndex for snapshot support.
func (rf *Raft) lastLogTerm() int {
	idx := rf.lastLogIndex()
	return rf.getTermAtIndex(idx)
}

// advanceCommitIndex attempts to advance commitIndex based on matchIndex of followers.
// Must be called with rf.mu held.
// Figure 2: Leaders - If there exists an N such that N > commitIndex, a majority
// of matchIndex[i] >= N, and log[N].term == currentTerm: set commitIndex = N.
func (rf *Raft) advanceCommitIndex() {
	// Only leader can advance commitIndex this way
	if rf.state != Leader {
		return
	}

	// Find highest N where majority has replicated
	// Codex Phase 4: Don't try to commit entries in snapshot (N > lastIncludedIndex)
	for N := rf.lastLogIndex(); N > rf.commitIndex && N > rf.lastIncludedIndex; N-- {
		// CRITICAL SAFETY PROPERTY (Figure 2):
		// Raft never commits log entries from previous terms by counting replicas.
		// Only log entries from the leader's current term are committed by counting replicas.
		// Codex Phase 4: Use getTermAtIndex() for snapshot support
		if rf.getTermAtIndex(N) != rf.currentTerm {
			continue
		}

		// Count how many servers have replicated this entry
		count := 0
		for i := range rf.peers {
			if rf.matchIndex[i] >= N {
				count++
			}
		}

		// Check for majority (> len(peers)/2)
		if count > len(rf.peers)/2 {
			rf.commitIndex = N
			break
		}
	}
}

// sendHeartbeats continuously sends heartbeats/replication while this server is leader.
// This function runs in its own goroutine and exits when the server is no longer leader.
// Codex feedback: Keep 100ms loop, branch between empty (heartbeat) and populated (replication) entries.
func (rf *Raft) sendHeartbeats() {
	for !rf.killed() {
		rf.mu.Lock()
		// Exit if no longer leader
		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}

		// Capture current state for this round
		currentTerm := rf.currentTerm
		leaderId := rf.me
		leaderCommit := rf.commitIndex
		rf.mu.Unlock()

		// Send AppendEntries to all peers in parallel
		for i := range rf.peers {
			if i == rf.me {
				continue
			}

			go func(server int) {
				rf.mu.Lock()
				// Check if we're still leader
				if rf.state != Leader || rf.currentTerm != currentTerm {
					rf.mu.Unlock()
					return
				}

				// Codex Phase 4: Check if follower is so far behind that we need InstallSnapshot
				if rf.nextIndex[server] <= rf.lastIncludedIndex {
					// Send InstallSnapshot instead of AppendEntries
					snapshotArgs := InstallSnapshotArgs{
						Term:              currentTerm,
						LeaderId:          leaderId,
						LastIncludedIndex: rf.lastIncludedIndex,
						LastIncludedTerm:  rf.lastIncludedTerm,
						Data:              rf.snapshot,
					}
					rf.mu.Unlock()

					snapshotReply := InstallSnapshotReply{}
					ok := rf.sendInstallSnapshot(server, &snapshotArgs, &snapshotReply)

					if ok {
						rf.mu.Lock()
						// Check if still leader with same term
						if rf.state != Leader || rf.currentTerm != currentTerm {
							rf.mu.Unlock()
							return
						}

						// Handle reply
						if snapshotReply.Term > rf.currentTerm {
							rf.currentTerm = snapshotReply.Term
							rf.state = Follower
							rf.votedFor = -1
							rf.persist()
							rf.mu.Unlock()
							return
						}

						// Update nextIndex and matchIndex
						rf.nextIndex[server] = rf.lastIncludedIndex + 1
						rf.matchIndex[server] = rf.lastIncludedIndex
						rf.mu.Unlock()
					}
					return // Skip AppendEntries for this server
				}

				// Codex Phase 4: Determine prevLogIndex and prevLogTerm using helpers
				prevLogIndex := rf.nextIndex[server] - 1
				prevLogTerm := rf.getTermAtIndex(prevLogIndex)

				// Codex Phase 4: Use logIndexToSlice to translate nextIndex to slice index
				// Branch between heartbeat (empty) and replication (populated)
				entries := []LogEntry{}
				if rf.nextIndex[server] <= rf.lastLogIndex() {
					// Follower needs entries - copy them
					sliceStart := rf.logIndexToSlice(rf.nextIndex[server])
					entries = make([]LogEntry, len(rf.log[sliceStart:]))
					copy(entries, rf.log[sliceStart:])
				}

				rf.mu.Unlock()

				// Send RPC (without holding lock)
				args := AppendEntriesArgs{
					Term:         currentTerm,
					LeaderId:     leaderId,
					PrevLogIndex: prevLogIndex,
					PrevLogTerm:  prevLogTerm,
					Entries:      entries,
					LeaderCommit: leaderCommit,
				}
				reply := AppendEntriesReply{}

				ok := rf.sendAppendEntries(server, &args, &reply)

				if ok {
					rf.mu.Lock()
					defer rf.mu.Unlock()

					// Ignore stale responses
					if rf.state != Leader || rf.currentTerm != currentTerm {
						return
					}

					// Step down if we see a higher term
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.state = Follower
						rf.votedFor = -1
						rf.persist()
						return
					}

					// Handle success/failure
					if reply.Success {
						// Update nextIndex and matchIndex for this follower
						// Set to index after the last entry we just sent
						newNext := prevLogIndex + len(entries) + 1
						newMatch := prevLogIndex + len(entries)

						// Only update if this response is more recent than what we have
						if newNext > rf.nextIndex[server] {
							rf.nextIndex[server] = newNext
						}
						if newMatch > rf.matchIndex[server] {
							rf.matchIndex[server] = newMatch
						}

						// Try to advance commitIndex based on new matchIndex
						rf.advanceCommitIndex()
					} else {
						// Consistency check failed - use fast backup optimization
						// RAFT paper page 8 (gray box): Use XTerm, XIndex, XLen for fast recovery

						if reply.XTerm == -1 {
							// Case 1: Follower's log is too short
							// Jump to follower's log length
							rf.nextIndex[server] = reply.XLen
						} else {
							// Case 2: Follower has conflicting entry at prevLogIndex
							// Search leader's log for XTerm
							// Codex feedback: Stop search at index 1 (don't check dummy entry at index 0)
								foundXTerm := false
								for i := rf.lastLogIndex(); i > rf.lastIncludedIndex; i-- {
									if rf.getTermAtIndex(i) == reply.XTerm {
										// Leader has XTerm - jump to last entry in that term + 1
										rf.nextIndex[server] = i + 1
										foundXTerm = true
										break
									}
								}
							if !foundXTerm {
								// Leader doesn't have XTerm - jump to follower's first index with XTerm
								rf.nextIndex[server] = reply.XIndex
							}
						}

						// Codex feedback: Guard against going below 1
						if rf.nextIndex[server] < 1 {
							rf.nextIndex[server] = 1
						}
						// Next heartbeat iteration will retry with earlier entries
					}
				}
			}(i)
		}

		// Sleep for ~100ms to send ~10 heartbeats per second (performance requirement)
		time.Sleep(100 * time.Millisecond)
	}
}

func (rf *Raft) ticker() {
	for rf.killed() == false {

		// Your code here (3A)
		// Check if a leader election should be started.

		// Check if election timeout has elapsed
		// Codex feedback: Use stored electionTimeout instead of re-sampling each loop
		rf.mu.Lock()
		timeSinceHeartbeat := time.Since(rf.lastHeartbeat)
		shouldStartElection := rf.state != Leader && timeSinceHeartbeat > rf.electionTimeout
		rf.mu.Unlock()

		if shouldStartElection {
			// Start election
			rf.mu.Lock()
			rf.state = Candidate
			rf.currentTerm++
			rf.votedFor = rf.me
			rf.resetElectionTimer() // Reset timer with new random timeout (Codex feedback)
			rf.persist()            // Persist state change

			currentTerm := rf.currentTerm
			lastLogIndex := rf.lastLogIndex()
			lastLogTerm := rf.lastLogTerm()
			rf.mu.Unlock()

			// Send RequestVote RPCs to all peers in parallel
			votesReceived := 1 // Vote for self
			var votesMu sync.Mutex

			for i := range rf.peers {
				if i == rf.me {
					continue
				}

				go func(server int) {
					args := RequestVoteArgs{
						Term:         currentTerm,
						CandidateId:  rf.me,
						LastLogIndex: lastLogIndex,
						LastLogTerm:  lastLogTerm,
					}
					reply := RequestVoteReply{}

					ok := rf.sendRequestVote(server, &args, &reply)

					if ok {
						rf.mu.Lock()
						defer rf.mu.Unlock()

						// If term has changed, ignore this reply
						if rf.currentTerm != currentTerm || rf.state != Candidate {
							return
						}

						// Update term if we see a higher one
						if reply.Term > rf.currentTerm {
							rf.currentTerm = reply.Term
							rf.state = Follower
							rf.votedFor = -1
							rf.persist()
							return
						}

						// Count vote if granted
						if reply.VoteGranted {
							votesMu.Lock()
							votesReceived++
							votes := votesReceived
							votesMu.Unlock()

							// Check if we have majority
				if votes > len(rf.peers)/2 && rf.state == Candidate {
					rf.state = Leader
					// Initialize leader state (Figure 2)
					rf.nextIndex = make([]int, len(rf.peers))
					rf.matchIndex = make([]int, len(rf.peers))
					lastLog := rf.lastLogIndex()
					for j := range rf.peers {
						rf.nextIndex[j] = lastLog + 1
						rf.matchIndex[j] = rf.lastIncludedIndex
					}
					// Codex feedback: Initialize self bookkeeping immediately
					// Leader's own log is already fully replicated locally
					rf.matchIndex[rf.me] = lastLog
					rf.nextIndex[rf.me] = lastLog + 1
					// Start sending heartbeats immediately
					go rf.sendHeartbeats()
				}
						}
					}
				}(i)
			}
		}

		// pause for a random amount of time between 50 and 350
		// milliseconds.
		ms := 50 + (rand.Int63() % 300)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
func Make(peers []*labrpc.ClientEnd, me int,
	persister *tester.Persister, applyCh chan raftapi.ApplyMsg) raftapi.Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (3A, 3B, 3C).

	// Seed RNG once (Codex feedback)
	rand.Seed(int64(me) + time.Now().UnixNano())

	// Initialize persistent state (Figure 2)
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = make([]LogEntry, 0)
	rf.log = append(rf.log, LogEntry{Term: 0, Command: nil}) // Dummy entry at index 0

	// Initialize volatile state (Figure 2)
	rf.commitIndex = 0
	rf.lastApplied = 0

	// Codex Phase 4: Initialize snapshot state
	rf.lastIncludedIndex = 0
	rf.lastIncludedTerm = 0
	rf.snapshot = nil

	// Initialize custom state
	rf.state = Follower
	rf.applyCh = applyCh

	// Sample initial election timeout (Codex feedback: must hold lock)
	rf.mu.Lock()
	rf.resetElectionTimer()
	rf.mu.Unlock()

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// Persist initial state (building muscle memory - Codex feedback)
	rf.persist()

	// start ticker goroutine to start elections
	go rf.ticker()

	// Start applier goroutine to send committed entries to state machine
	// Codex feedback: Fast catch-up pattern - only sleep when nothing to apply
	// Codex Phase 4: Handle snapshot delivery
	go func() {
		for !rf.killed() {
			rf.mu.Lock()

			// Codex Phase 4: Deliver snapshot before log entries when needed
			if rf.snapshotPending || rf.lastApplied < rf.lastIncludedIndex {
				snapshotIndex := rf.lastIncludedIndex
				snapshotTerm := rf.lastIncludedTerm
				var snapshotCopy []byte
				if rf.snapshot != nil {
					snapshotCopy = make([]byte, len(rf.snapshot))
					copy(snapshotCopy, rf.snapshot)
				}
				rf.snapshotPending = false
				rf.lastApplied = snapshotIndex
				snapshotMsg := raftapi.ApplyMsg{
					SnapshotValid: true,
					Snapshot:      snapshotCopy,
					SnapshotTerm:  snapshotTerm,
					SnapshotIndex: snapshotIndex,
				}
				rf.mu.Unlock()
				rf.applyCh <- snapshotMsg
				continue
			}

			// Check if there are entries to apply
			if rf.commitIndex > rf.lastApplied {
				// Apply the next entry
				rf.lastApplied++
				// Codex Phase 4: Use logIndexToSlice() for snapshot support
				sliceIndex := rf.logIndexToSlice(rf.lastApplied)
				entry := rf.log[sliceIndex]
				applyMsg := raftapi.ApplyMsg{
					CommandValid: true,
					Command:      entry.Command,
					CommandIndex: rf.lastApplied,
				}

				rf.mu.Unlock()
				// Send without holding lock to avoid deadlock if applyCh blocks
				rf.applyCh <- applyMsg
				// Continue immediately for fast catch-up (Codex feedback)
				continue
			}

			rf.mu.Unlock()
			// Only sleep when nothing to apply
			time.Sleep(10 * time.Millisecond)
		}
	}()

	return rf
}
