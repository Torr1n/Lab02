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

// enum for the states of a raft server
type ServerState int

const (
	Follower  ServerState = 0
	Candidate ServerState = 1
	Leader    ServerState = 2
)

// object for entries in the log
type LogEntry struct {
	Term    int         // term when entry was received by leader
	Command interface{} // command for state machine
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

	// persistent state on all servers (Figure 2)
	currentTerm int        // latest term server has seen (init to 0)
	votedFor    int        // candidate id that received vote in current term (init to -1)
	log         []LogEntry // log entries

	// volatile state on all servers (Figure 2)
	commitIndex int // index of highest log entry known to be committed (init to 0)
	lastApplied int // index of highest log entry applied to state machine (init to 0)

	// volatile state on leaders (Figure 2)
	// init after election
	nextIndex  []int // for each follower index of next log entry to send (init to leader last log index + 1)
	matchIndex []int // for each follower index of highest log entry known to be replicated (init to 0)

	// snapshot state to track the last log entry included in the snapshot
	lastIncludedIndex int    // index of last entry in snapshot
	lastIncludedTerm  int    // term of last entry in snapshot
	snapshot          []byte // snapshot raw data
	snapshotPending   bool   // true when a freshly installed snapshot must be delivered to applyCh

	// custom fields
	state           ServerState   // current server state (Follower, Candidate, or Leader)
	lastHeartbeat   time.Time     // time of last heartbeat received (for election timeout)
	electionTimeout time.Duration // randomized election timeout 
	applyCh         chan raftapi.ApplyMsg // channel to send committed entries to service
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
	// must be updated on stable storage before responding to RPCs
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)

	// encode persistent state variables 
	if err := e.Encode(rf.currentTerm); err != nil {
		panic("Failed to encode currentTerm: " + err.Error())
	}
	if err := e.Encode(rf.votedFor); err != nil {
		panic("Failed to encode votedFor: " + err.Error())
	}
	if err := e.Encode(rf.log); err != nil {
		panic("Failed to encode log: " + err.Error())
	}

	// also encode snapshot metadata
	if err := e.Encode(rf.lastIncludedIndex); err != nil {
		panic("Failed to encode lastIncludedIndex: " + err.Error())
	}
	if err := e.Encode(rf.lastIncludedTerm); err != nil {
		panic("Failed to encode lastIncludedTerm: " + err.Error())
	}

	raftstate := w.Bytes()
	// save snapshot alongside raftstate
	rf.persister.Save(raftstate, rf.snapshot)
}


// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}

	// decode persistent state (matching encode order)
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)

	// def types to decode into
	var currentTerm int
	var votedFor int
	var log []LogEntry
	var lastIncludedIndex int
	var lastIncludedTerm int

	// check decode errors and panic
	if err := d.Decode(&currentTerm); err != nil {
		panic("Failed to decode currentTerm: " + err.Error())
	}
	if err := d.Decode(&votedFor); err != nil {
		panic("Failed to decode votedFor: " + err.Error())
	}
	if err := d.Decode(&log); err != nil {
		panic("Failed to decode log: " + err.Error())
	}

	// also decode snapshot metadata
	if err := d.Decode(&lastIncludedIndex); err != nil {
		// no snapshot data (bootstrap case)
		lastIncludedIndex = 0
		lastIncludedTerm = 0
	} else {
		if err := d.Decode(&lastIncludedTerm); err != nil {
			panic("Failed to decode lastIncludedTerm: " + err.Error())
		}
	}

	// restore state
	rf.currentTerm = currentTerm
	rf.votedFor = votedFor
	rf.log = log
	rf.lastIncludedIndex = lastIncludedIndex
	rf.lastIncludedTerm = lastIncludedTerm
	rf.snapshot = rf.persister.ReadSnapshot()

	// 1. make sure log has base entry (after snapshot, log[0] = snapshot base, not dummy)
	if len(rf.log) == 0 {
		rf.log = append(rf.log, LogEntry{Term: rf.lastIncludedTerm, Command: nil})
	}

	// 2. clamp commitIndex and lastApplied to last log index
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

	// ignore if we alr snapshotted past this point
	if index <= rf.lastIncludedIndex {
		return
	}

	// get term at snapshot point, rebuild log with base entry
	snapshotTerm := rf.getTermAtIndex(index)

	// trim log to keep entries AFTER index
	// rebuild with snapshot base entry at position 0
	sliceIndex := rf.logIndexToSlice(index)
	newLog := make([]LogEntry, 0)
	newLog = append(newLog, LogEntry{Term: snapshotTerm, Command: nil}) // snapshot base
	if sliceIndex+1 < len(rf.log) {
		newLog = append(newLog, rf.log[sliceIndex+1:]...)
	}
	rf.log = newLog

	// update snapshot metadata
	rf.lastIncludedIndex = index
	rf.lastIncludedTerm = snapshotTerm
	if snapshot != nil {
		snapshotCopy := make([]byte, len(snapshot))
		copy(snapshotCopy, snapshot)
		rf.snapshot = snapshotCopy
	} else {
		rf.snapshot = nil
	}

	// update commitIndex and lastApplied if they fall below snapshot
	if rf.commitIndex < rf.lastIncludedIndex {
		rf.commitIndex = rf.lastIncludedIndex
	}
	if rf.lastApplied < rf.lastIncludedIndex {
		rf.lastApplied = rf.lastIncludedIndex
	}

	// persist new state (save snapshot bytes)
	rf.persist()
}


// RequestVote RPC args (Figure 2).
// field names must start with capital letters!
type RequestVoteArgs struct {
	Term         int // candidate term
	CandidateId  int // candidate requesting vote
	LastLogIndex int // index of candidate last log entry
	LastLogTerm  int // term of candidate last log entry
}

// RequestVote RPC reply struct (Figure 2).
// field names must start with capital letters!
type RequestVoteReply struct {
	Term        int  // currentTerm, for candidate to update itself
	VoteGranted bool // true means candidate received vote
}

// RequestVote RPC handler (Figure 2).
// invoked by candidates to gather votes
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// reply false if term < currentTerm
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
		return
	}

	// if RPC request or response contains term T > currentTerm
	// set currentTerm = T + convert to follower
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist() // persist state change
	}

	// get last log index and term for up to date check 
	// use snapshot helpers to adjust
	lastLogIndex := rf.lastLogIndex()
	lastLogTerm := rf.lastLogTerm()

	// check if candidate's log is at least as up to date
	logIsUpToDate := false
	if args.LastLogTerm > lastLogTerm {
		logIsUpToDate = true
	} else if args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex {
		logIsUpToDate = true
	}

	// grant vote if
	// - votedFor is null (-1) or candidateId and
	// - candidate log is at least as up to date as receiver log
	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && logIsUpToDate {
		rf.votedFor = args.CandidateId
		rf.resetElectionTimer() // reset election timer when granting vote
		rf.persist()            // persist vote bwfore replying
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

// AppendEntries RPC args struct (Figure 2)
type AppendEntriesArgs struct {
	Term         int        // leader term
	LeaderId     int        // so follower can redirect clients
	PrevLogIndex int        // index of log entry immediately preceding new ones
	PrevLogTerm  int        // term of prevLogIndex entry
	Entries      []LogEntry // log entries to store empty for hb
	LeaderCommit int        // leader commitIndex
}

// AppendEntries RPC reply struct (Figure 2)
type AppendEntriesReply struct {
	Term    int  // currentTerm in case leader needs to update itself to follower
	Success bool // true if follower contained entry matching prevLogIndex and prevLogTerm

	// fast backup (RAFT paper pg8)
	// leader can quickly find the right log index when follower logs diverge
	XTerm  int // term of the conflicting entry (or -1 if log too short)
	XIndex int // index of first entry with term XTerm
	XLen   int // log length (when log too short)
}

// AppendEntries RPC handler (Figure 2)
// called by leader to replicate log entries + used as heartbeat
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// reply false if term < currentTerm - shouldnt be leader
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.Success = false
		return
	}

	// if RPC request contains term T > currentTerm
	// set currentTerm = T convert to follower to prevent double voting within the same term
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist() // persist state change
	} else if rf.state == Candidate || rf.state == Leader {
		// if we're candidate or leader and receive AppendEntries with equal term
		// recognize the leader and step down
		rf.state = Follower
	}

	// reset election timer when receiving valid AppendEntries
	// prevents followers from starting unnecessary elections
	rf.resetElectionTimer()

	// next 3 if statements - "reply false if log doesn't contain entry at prevLogIndex whose term matches prevLogTerm"

	// check if prevLogIndex is in snapshot (leader is behind)
	if args.PrevLogIndex < rf.lastIncludedIndex {
		reply.Success = false
		reply.Term = rf.currentTerm
		reply.XTerm = -1
		reply.XLen = rf.lastIncludedIndex
		reply.XIndex = -1
		return
	}

	// log too short
	if args.PrevLogIndex > rf.lastLogIndex() {
		reply.Success = false
		reply.Term = rf.currentTerm
		// fast backup optimization
		reply.XTerm = -1
		reply.XLen = rf.lastLogIndex() + 1 // logical length
		reply.XIndex = -1
		return
	}

	// term mismatch at prevLogIndex
	if args.PrevLogIndex >= 0 {
		prevLogTerm := rf.getTermAtIndex(args.PrevLogIndex)
		if prevLogTerm != args.PrevLogTerm {
			reply.Success = false
			reply.Term = rf.currentTerm
			// fast backup optimization
			reply.XTerm = prevLogTerm
			reply.XLen = rf.lastLogIndex() + 1 // logical length
			// find first index with term XTerm
			reply.XIndex = args.PrevLogIndex
			for reply.XIndex > rf.lastIncludedIndex &&
				rf.getTermAtIndex(reply.XIndex-1) == reply.XTerm {
				reply.XIndex--
			}
			return
		}
	}

	// DELETE CONFLICTING ENTRIES
	// if an existing entry conflicts with a new one (same index but different terms),
	// delete the existing entry and all that follow it
	for i, entry := range args.Entries {
		logicalIndex := args.PrevLogIndex + 1 + i
		if logicalIndex <= rf.lastIncludedIndex {
			continue // entry already in snapshot
		}
		if rf.hasLogEntry(logicalIndex) {
			sliceIndex := rf.logIndexToSlice(logicalIndex)
			if rf.log[sliceIndex].Term != entry.Term {
				// conflict so truncate log from this point
				rf.log = rf.log[:sliceIndex]
				rf.persist() // Persist truncation
				break
			}
		}
	}

	// APPEND NEW ENTRIES
	// append any new entries not already in the log
	for i, entry := range args.Entries {
		logicalIndex := args.PrevLogIndex + 1 + i
		if logicalIndex <= rf.lastIncludedIndex {
			continue // entry already in snapshot
		}
		if !rf.hasLogEntry(logicalIndex) {
			// entry doesn't exist so append it
			rf.log = append(rf.log, entry)
		}
	}

	// persist if we appended any entries
	if len(args.Entries) > 0 {
		rf.persist()
	}

	// UPDATE COMMIT INDEX
	// if leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry)
	if args.LeaderCommit > rf.commitIndex {
		// use min to avoid committing beyond our log
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

// sendAppendEntries sends a AppendEntries RPC to a follower server
func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

// InstallSnapshot RPC args struct (RAFT paper Figure 13)
type InstallSnapshotArgs struct {
	Term              int    // leaders term
	LeaderId          int    // so follower can redirect clients
	LastIncludedIndex int    // snapshot replaces all entries up through and including this index
	LastIncludedTerm  int    // term of lastIncludedIndex
	Data              []byte // raw bytes of the snapshot
}

// InstallSnapshot RPC reply struct
type InstallSnapshotReply struct {
	Term int // currentTerm for leader to update itself
}

// InstallSnapshot RPC handler
// invoked by leader to send snapshot to followers that fell behind
func (rf *Raft) InstallSnapshot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// reply with currentTerm
	reply.Term = rf.currentTerm

	// reject if term < currentTerm
	if args.Term < rf.currentTerm {
		return
	}

	// update term if we see higher term
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist()
	}

	// reset election timer
	rf.resetElectionTimer()

	// ignore if we already have this snapshot
	if args.LastIncludedIndex <= rf.lastIncludedIndex {
		return
	}

	// discard log entries covered by snapshot
	// rebuild log with snapshot base entry
	newLog := make([]LogEntry, 0)
	newLog = append(newLog, LogEntry{Term: args.LastIncludedTerm, Command: nil}) // snapshot base

	// keep any entries after the snapshot that we have
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

	// update commitIndex and lastApplied
	if rf.commitIndex < rf.lastIncludedIndex {
		rf.commitIndex = rf.lastIncludedIndex
	}
	if rf.lastApplied < rf.lastIncludedIndex {
		rf.lastApplied = rf.lastIncludedIndex
	}

	// persist new state
	rf.persist()
}

// sendInstallSnapshot sends a InstallSnapshot RPC to a follower server
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

	// compute LOGICAL index first, BEFORE appending
	// index the client will see when entry is committed
	index := rf.lastLogIndex() + 1
	term := rf.currentTerm

	// append command to local log with current term
	entry := LogEntry{
		Term:    term,
		Command: command,
	}
	rf.log = append(rf.log, entry)

	// leader knows it has this entry, no need to wait for heartbeat
	rf.matchIndex[rf.me] = index
	rf.nextIndex[rf.me] = index + 1

	// persist the updated log
	rf.persist()

	// return LOGICAL index
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

// resetElectionTimer resets the election timer with a new randomized timeout, called with lock held
func (rf *Raft) resetElectionTimer() {
	rf.lastHeartbeat = time.Now()
	// use 150-300ms range to match Figure 2
	rf.electionTimeout = time.Duration(150+rand.Intn(150)) * time.Millisecond
}

// logIndexToSlice converts logical log index to slice index, called with lock held
// after snapshots, log[0] represents lastIncludedIndex, not index 0
func (rf *Raft) logIndexToSlice(logicalIndex int) int {
	return logicalIndex - rf.lastIncludedIndex
}

// hasLogEntry checks if we have the entry at logicalIndex
// returns false if entry is in snapshot (index <= lastIncludedIndex) or doesn't exist, called with lock held
func (rf *Raft) hasLogEntry(logicalIndex int) bool {
	if logicalIndex <= rf.lastIncludedIndex {
		return false // entry is in snapshot
	}
	if logicalIndex > rf.lastLogIndex() {
		return false // entry doesn't exist yet
	}
	return true
}

// getTermAtIndex returns term at logical index
// handles both snapshot base (lastIncludedIndex) and regular log entries, called with lock held
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

// lastLogIndex returns the LOGICAL index of the last log entry, called with lock held
func (rf *Raft) lastLogIndex() int {
	return rf.lastIncludedIndex + len(rf.log) - 1
}

// lastLogTerm returns the term of the last log entry, called with lock held
func (rf *Raft) lastLogTerm() int {
	idx := rf.lastLogIndex()
	return rf.getTermAtIndex(idx)
}

// advanceCommitIndex attempts to advance commitIndex based on matchIndex of followers, called with lock held
// Figure 2: "If there exists an N such that N > commitIndex, a majority
// of matchIndex[i] >= N, and log[N].term == currentTerm: set commitIndex = N"
func (rf *Raft) advanceCommitIndex() {
	// only leader can advance commitIndex
	if rf.state != Leader {
		return
	}

	// find highest N where majority has replicated
	// don't try to commit entries in snapshot (N > lastIncludedIndex)
	for N := rf.lastLogIndex(); N > rf.commitIndex && N > rf.lastIncludedIndex; N-- {
		// never commit log entries from previous terms by counting replicas
		// only log entries from the leader's current term are committed by counting replicas
		if rf.getTermAtIndex(N) != rf.currentTerm {
			continue
		}

		// count how many servers have replicated this entry
		count := 0
		for i := range rf.peers {
			if rf.matchIndex[i] >= N {
				count++
			}
		}

		// check for majority (> len(peers)/2)
		if count > len(rf.peers)/2 {
			rf.commitIndex = N
			break
		}
	}
}

// sendHeartbeats continuously sends heartbeats/replication while this server is leader
// runs in its own goroutine and exits when the server is no longer leader
func (rf *Raft) sendHeartbeats() {
	for !rf.killed() {
		rf.mu.Lock()
		// exit if no longer leader
		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}

		// capture current state for this round
		currentTerm := rf.currentTerm
		leaderId := rf.me
		leaderCommit := rf.commitIndex
		rf.mu.Unlock()

		// send AppendEntries to all peers in parallel
		for i := range rf.peers {
			if i == rf.me {
				continue
			}

			go func(server int) {
				rf.mu.Lock()
				// check if we're still leader
				if rf.state != Leader || rf.currentTerm != currentTerm {
					rf.mu.Unlock()
					return
				}

				// check if follower is so far behind that we need InstallSnapshot
				if rf.nextIndex[server] <= rf.lastIncludedIndex {
					// send InstallSnapshot instead of AppendEntries
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
						// check if still leader with same term
						if rf.state != Leader || rf.currentTerm != currentTerm {
							rf.mu.Unlock()
							return
						}

						// handle reply
						if snapshotReply.Term > rf.currentTerm {
							rf.currentTerm = snapshotReply.Term
							rf.state = Follower
							rf.votedFor = -1
							rf.persist()
							rf.mu.Unlock()
							return
						}

						// update nextIndex and matchIndex
						rf.nextIndex[server] = rf.lastIncludedIndex + 1
						rf.matchIndex[server] = rf.lastIncludedIndex
						rf.mu.Unlock()
					}
					return // skip AppendEntries for this server
				}

				// determine prevLogIndex and prevLogTerm using helpers
				prevLogIndex := rf.nextIndex[server] - 1
				prevLogTerm := rf.getTermAtIndex(prevLogIndex)

				// use logIndexToSlice to translate nextIndex to slice index
				// branch between heartbeat (empty) and replication (populated)
				entries := []LogEntry{}
				if rf.nextIndex[server] <= rf.lastLogIndex() {
					// follower needs entries so copy them
					sliceStart := rf.logIndexToSlice(rf.nextIndex[server])
					entries = make([]LogEntry, len(rf.log[sliceStart:]))
					copy(entries, rf.log[sliceStart:])
				}

				rf.mu.Unlock()

				// send RPC (without holding lock)
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

					// ignore stale responses
					if rf.state != Leader || rf.currentTerm != currentTerm {
						return
					}

					// step down if we see a higher term
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.state = Follower
						rf.votedFor = -1
						rf.persist()
						return
					}

					// handle success/failure
					if reply.Success {
						// update nextIndex and matchIndex for this follower
						// set to index after the last entry we just sent
						newNext := prevLogIndex + len(entries) + 1
						newMatch := prevLogIndex + len(entries)

						// only update if this response is more recent than what we have
						if newNext > rf.nextIndex[server] {
							rf.nextIndex[server] = newNext
						}
						if newMatch > rf.matchIndex[server] {
							rf.matchIndex[server] = newMatch
						}

						// try to advance commitIndex based on new matchIndex
						rf.advanceCommitIndex()
					} else {
						// consistency check failed so use fast backup with XTerm, XIndex and XLen

						if reply.XTerm == -1 {
							// case 1: follower's log is too short
							// jump to follower's log length
							rf.nextIndex[server] = reply.XLen
						} else {
							// case 2: follower has conflicting entry at prevLogIndex
							// search leader's log for XTerm
								foundXTerm := false
								for i := rf.lastLogIndex(); i > rf.lastIncludedIndex; i-- {
									if rf.getTermAtIndex(i) == reply.XTerm {
										// leader has XTerm so jump to last entry in that term + 1
										rf.nextIndex[server] = i + 1
										foundXTerm = true
										break
									}
								}
							if !foundXTerm {
								// leader doesn't have XTerm so jump to follower's first index with XTerm
								rf.nextIndex[server] = reply.XIndex
							}
						}

						// guard against going below 1
						if rf.nextIndex[server] < 1 {
							rf.nextIndex[server] = 1
						}
						// next heartbeat iteration will retry with earlier entries
					}
				}
			}(i)
		}

		// sleep for ~100ms to send ~10 heartbeats per second
		time.Sleep(100 * time.Millisecond)
	}
}

func (rf *Raft) ticker() {
	for rf.killed() == false {

		// Your code here (3A)
		// Check if a leader election should be started.

		// check if election timeout has elapsed
		rf.mu.Lock()
		timeSinceHeartbeat := time.Since(rf.lastHeartbeat)
		shouldStartElection := rf.state != Leader && timeSinceHeartbeat > rf.electionTimeout
		rf.mu.Unlock()

		if shouldStartElection {
			// start election
			rf.mu.Lock()
			rf.state = Candidate
			rf.currentTerm++
			rf.votedFor = rf.me
			rf.resetElectionTimer() // reset timer with new random timeout for next election
			rf.persist()            // persist state change

			currentTerm := rf.currentTerm
			lastLogIndex := rf.lastLogIndex()
			lastLogTerm := rf.lastLogTerm()
			rf.mu.Unlock()

			// send RequestVote RPCs to all peers in parallel
			votesReceived := 1 // vote for self
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

						// if term has changed, ignore this reply
						if rf.currentTerm != currentTerm || rf.state != Candidate {
							return
						}

						// update term if we see a higher one
						if reply.Term > rf.currentTerm {
							rf.currentTerm = reply.Term
							rf.state = Follower
							rf.votedFor = -1
							rf.persist()
							return
						}

						// count vote if granted
						if reply.VoteGranted {
							votesMu.Lock()
							votesReceived++
							votes := votesReceived
							votesMu.Unlock()

						// check if we have majority
				if votes > len(rf.peers)/2 && rf.state == Candidate {
					rf.state = Leader
					// init leader state
					rf.nextIndex = make([]int, len(rf.peers))
					rf.matchIndex = make([]int, len(rf.peers))
					lastLog := rf.lastLogIndex()
					for j := range rf.peers {
						rf.nextIndex[j] = lastLog + 1
						rf.matchIndex[j] = rf.lastIncludedIndex
					}
					// leaders own log is already fully replicated locally
					rf.matchIndex[rf.me] = lastLog
					rf.nextIndex[rf.me] = lastLog + 1
					// start sending heartbeats immediately
					go rf.sendHeartbeats()
				}
						}
					}
				}(i)
			}
		}

		// pause for a random amount of time between 50 and 350 ms
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

	// seed RNG once
	rand.Seed(int64(me) + time.Now().UnixNano())

	// init persistent state 
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = make([]LogEntry, 0)
	rf.log = append(rf.log, LogEntry{Term: 0, Command: nil}) // Dummy entry at index 0

	// init volatile state 
	rf.commitIndex = 0
	rf.lastApplied = 0

	// init snapshot state
	rf.lastIncludedIndex = 0
	rf.lastIncludedTerm = 0
	rf.snapshot = nil

	// init custom state
	rf.state = Follower
	rf.applyCh = applyCh

	// sample initial election timeout 
	rf.mu.Lock()
	rf.resetElectionTimer()
	rf.mu.Unlock()

	// init from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// persist initial state
	rf.persist()

	// start ticker goroutine to start elections
	go rf.ticker()

	// start applier goroutine to send committed entries to state machine
	go func() {
		for !rf.killed() {
			rf.mu.Lock()

			// deliver snapshot before log entries when needed
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

			// check if there are entries to apply
			if rf.commitIndex > rf.lastApplied {
				// apply the next entry
				rf.lastApplied++
				// use logIndexToSlice() for snapshot support
				sliceIndex := rf.logIndexToSlice(rf.lastApplied)
				entry := rf.log[sliceIndex]
				applyMsg := raftapi.ApplyMsg{
					CommandValid: true,
					Command:      entry.Command,
					CommandIndex: rf.lastApplied,
				}

				rf.mu.Unlock()
				// send without holding lock to avoid deadlock if applyCh blocks
				rf.applyCh <- applyMsg
				// continue immediately for fast catch-up
				continue
			}

			rf.mu.Unlock()
			// Only sleep when nothing to apply
			time.Sleep(10 * time.Millisecond)
		}
	}()

	return rf
}
