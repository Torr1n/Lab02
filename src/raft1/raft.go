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
	state         ServerState // Current server state (Follower, Candidate, or Leader)
	lastHeartbeat time.Time   // Time of last heartbeat received (for election timeout)
	applyCh       chan raftapi.ApplyMsg // Channel to send committed entries to service
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

	// Get last log index and term for up-to-date check
	lastLogIndex := len(rf.log) - 1
	lastLogTerm := 0
	if lastLogIndex >= 0 {
		lastLogTerm = rf.log[lastLogIndex].Term
	}

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
		rf.lastHeartbeat = time.Now() // Reset election timer when granting vote (Codex feedback)
		rf.persist()                  // Persist vote
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

	// If RPC request contains term T >= currentTerm:
	// set currentTerm = T, convert to follower (Figure 2: Rules for Servers - All Servers)
	if args.Term >= rf.currentTerm {
		rf.currentTerm = args.Term
		rf.votedFor = -1
		rf.state = Follower
		rf.persist() // Persist state change
	}

	// Reset election timer when receiving valid AppendEntries (Codex feedback)
	// This prevents followers from starting unnecessary elections
	rf.lastHeartbeat = time.Now()

	// For Phase 3A (heartbeat only), just reply success
	// Phase 3B will add log consistency check and replication logic
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
	index := -1
	term := -1
	isLeader := true

	// Your code here (3B).


	return index, term, isLeader
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

// sendHeartbeats continuously sends heartbeats while this server is leader.
// This function runs in its own goroutine and exits when the server is no longer leader.
func (rf *Raft) sendHeartbeats() {
	for !rf.killed() {
		rf.mu.Lock()
		// Exit if no longer leader
		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}

		// Prepare heartbeat arguments
		currentTerm := rf.currentTerm
		leaderId := rf.me
		leaderCommit := rf.commitIndex
		rf.mu.Unlock()

		// Send AppendEntries (heartbeat) to all peers in parallel
		for i := range rf.peers {
			if i == rf.me {
				continue
			}

			go func(server int) {
				rf.mu.Lock()
				// Get prevLog info for this peer (will be used in Phase 3B)
				prevLogIndex := len(rf.log) - 1
				prevLogTerm := 0
				if prevLogIndex >= 0 {
					prevLogTerm = rf.log[prevLogIndex].Term
				}
				rf.mu.Unlock()

				args := AppendEntriesArgs{
					Term:         currentTerm,
					LeaderId:     leaderId,
					PrevLogIndex: prevLogIndex,
					PrevLogTerm:  prevLogTerm,
					Entries:      nil, // Empty for heartbeat
					LeaderCommit: leaderCommit,
				}
				reply := AppendEntriesReply{}

				ok := rf.sendAppendEntries(server, &args, &reply)

				if ok {
					rf.mu.Lock()
					defer rf.mu.Unlock()

					// If we've stepped down or term has changed, ignore this reply
					if rf.state != Leader || rf.currentTerm != currentTerm {
						return
					}

					// Step down if we see a higher term
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.state = Follower
						rf.votedFor = -1
						rf.persist()
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
		rf.mu.Lock()
		electionTimeout := time.Duration(300+rand.Intn(150)) * time.Millisecond // 300-450ms randomized
		timeSinceHeartbeat := time.Since(rf.lastHeartbeat)
		shouldStartElection := rf.state != Leader && timeSinceHeartbeat > electionTimeout
		rf.mu.Unlock()

		if shouldStartElection {
			// Start election
			rf.mu.Lock()
			rf.state = Candidate
			rf.currentTerm++
			rf.votedFor = rf.me
			rf.lastHeartbeat = time.Now() // Reset election timer
			rf.persist()                  // Persist state change

			currentTerm := rf.currentTerm
			lastLogIndex := len(rf.log) - 1
			lastLogTerm := 0
			if lastLogIndex >= 0 {
				lastLogTerm = rf.log[lastLogIndex].Term
			}
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
	rf.lastHeartbeat = time.Now()
	rf.applyCh = applyCh

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// Persist initial state (building muscle memory - Codex feedback)
	rf.persist()

	// start ticker goroutine to start elections
	go rf.ticker()


	return rf
}
