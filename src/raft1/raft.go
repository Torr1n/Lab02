package raft

// The file raftapi/raft.go defines the interface that raft must
// expose to servers (or the tester), but see comments below for each
// of these functions for more details.
//
// Make() creates a new raft peer that implements the raft interface.

import (
	//	"bytes"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	//	"cpsc416-2025w1/labgob"
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
	// Your code here (3C).
	// Example:
	// w := new(bytes.Buffer)
	// e := labgob.NewEncoder(w)
	// e.Encode(rf.xxx)
	// e.Encode(rf.yyy)
	// raftstate := w.Bytes()
	// rf.persister.Save(raftstate, nil)
}


// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (3C).
	// Example:
	// r := bytes.NewBuffer(data)
	// d := labgob.NewDecoder(r)
	// var xxx
	// var yyy
	// if d.Decode(&xxx) != nil ||
	//    d.Decode(&yyy) != nil {
	//   error...
	// } else {
	//   rf.xxx = xxx
	//   rf.yyy = yyy
	// }
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
	// Your code here (3D).

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

	// Check 1: Log too short (Codex feedback: bounds safety)
	if args.PrevLogIndex > rf.lastLogIndex() {
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	}

	// Check 2: Term mismatch at prevLogIndex
	// Handle prevLogIndex == -1 case (appending to empty log)
	if args.PrevLogIndex >= 0 && rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	}

	// DELETE CONFLICTING ENTRIES (Figure 2: Receiver Implementation 3)
	// If an existing entry conflicts with a new one (same index but different terms),
	// delete the existing entry and all that follow it
	for i, entry := range args.Entries {
		logIndex := args.PrevLogIndex + 1 + i
		if logIndex < len(rf.log) {
			// Entry exists at this index - check for conflict
			if rf.log[logIndex].Term != entry.Term {
				// Conflict detected: truncate log from this point
				rf.log = rf.log[:logIndex]
				rf.persist() // Persist truncation
				break
			}
		}
	}

	// APPEND NEW ENTRIES (Figure 2: Receiver Implementation 4)
	// Append any new entries not already in the log
	for i, entry := range args.Entries {
		logIndex := args.PrevLogIndex + 1 + i
		if logIndex >= len(rf.log) {
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

	// Append command to local log with current term (Figure 2: Leaders - Upon receiving command from client)
	index := len(rf.log)
	term := rf.currentTerm
	entry := LogEntry{
		Term:    term,
		Command: command,
	}
	rf.log = append(rf.log, entry)

	// Codex feedback: Update self bookkeeping immediately
	// Leader knows it has this entry, no need to wait for heartbeat
	rf.matchIndex[rf.me] = index
	rf.nextIndex[rf.me] = index + 1

	// Persist the updated log (Phase 3C will implement this)
	rf.persist()

	// Return the index where command will appear, current term, and isLeader=true
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

// lastLogIndex returns the index of the last log entry.
// Must be called with rf.mu held.
// Codex feedback: Helper to avoid repetitive len(log)-1 and dummy-entry checks.
func (rf *Raft) lastLogIndex() int {
	return len(rf.log) - 1
}

// lastLogTerm returns the term of the last log entry.
// Must be called with rf.mu held.
// Returns 0 if log is empty (only dummy entry at index 0).
func (rf *Raft) lastLogTerm() int {
	idx := rf.lastLogIndex()
	if idx < 0 {
		return 0
	}
	return rf.log[idx].Term
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
	// Codex feedback: Loop from len(log)-1 down to 1 (skip dummy entry at index 0)
	for N := rf.lastLogIndex(); N > rf.commitIndex && N >= 1; N-- {
		// CRITICAL SAFETY PROPERTY (Figure 2):
		// Raft never commits log entries from previous terms by counting replicas.
		// Only log entries from the leader's current term are committed by counting replicas.
		if rf.log[N].Term != rf.currentTerm {
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

				// Determine prevLogIndex and prevLogTerm for this peer
				prevLogIndex := rf.nextIndex[server] - 1
				prevLogTerm := 0
				if prevLogIndex >= 0 && prevLogIndex < len(rf.log) {
					prevLogTerm = rf.log[prevLogIndex].Term
				}

				// Codex feedback: Branch between heartbeat (empty) and replication (populated)
				// If follower is caught up, send empty heartbeat
				// Otherwise, send pending entries
				entries := []LogEntry{}
				if rf.nextIndex[server] < len(rf.log) {
					// Follower needs entries - send them
					// Copy entries to avoid holding lock during RPC
					entries = make([]LogEntry, len(rf.log[rf.nextIndex[server]:]))
					copy(entries, rf.log[rf.nextIndex[server]:])
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
						// Consistency check failed - decrement nextIndex and retry
						// Codex feedback: Guard with if nextIndex > 1 to prevent going to 0 or negative
						if rf.nextIndex[server] > 1 {
							rf.nextIndex[server]--
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
								for j := range rf.peers {
									rf.nextIndex[j] = len(rf.log)
									rf.matchIndex[j] = 0
								}
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
	go func() {
		for !rf.killed() {
			rf.mu.Lock()

			// Check if there are entries to apply
			if rf.commitIndex > rf.lastApplied {
				// Apply the next entry
				rf.lastApplied++
				entry := rf.log[rf.lastApplied]
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
