# Lab 2: Raft

### Introduction

In this lab, you'll implement Raft, a replicated state machine protocol. A replicated service achieves fault tolerance by storing complete copies of its state (i.e., data) on multiple replica servers. Replication allows the service to continue operating even if some of its servers experience failures (crashes or a broken or flaky network). The challenge is that failures may cause the replicas to hold differing copies of the data.

Raft organizes client requests into a sequence, called the log, and ensures that all the replica servers see the same log. Each replica executes client requests in log order, applying them to its local copy of the service's state. Since all the live replicas see the same log contents, they all execute the same requests in the same order, and thus continue to have identical service state. If a server fails but later recovers, Raft takes care of bringing its log up to date. Raft will continue to operate as long as at least a majority of the servers are alive and can talk to each other. If there is no such majority, Raft will make no progress, but will pick up where it left off as soon as a majority can communicate again.

In this lab you'll implement Raft as a Go object type with associated methods, able to be used as a module in a larger service. A set of Raft instances talk to each other with RPC to maintain replicated logs. Your Raft interface will support an indefinite sequence of numbered commands, also called log entries. The entries are numbered with _index numbers_. The log entry with a given index will eventually be committed. At that point, your Raft should send the log entry to the larger service for it to execute.

You should follow the design in the [extended Raft paper], with particular attention to Figure 2. You'll implement most of what's in the paper, including saving persistent state and reading it after a node fails and then restarts. You will not implement cluster membership changes (Section 6).

### Getting Started

We supply you with skeleton code src/raft1/raft.go. We also supply a set of tests, which you should use to drive your implementation efforts, and which we'll use to grade your submitted lab. The tests are in src/raft1/raft_test.go.

When we grade your submissions, we will run the tests without the [\-race flag](https://go.dev/blog/race-detector). However, you should check that your code does not have races, by running the tests with the \-race flag as you develop your solution.

To get up and running, execute the following commands.

    cd src/raft1
    go test
    Test (3A): initial election (reliable network)...
    Fatal: expected one leader, got none
    --- FAIL: TestInitialElection3A (4.90s)
    Test (3A): election after network failure (reliable network)...
    Fatal: expected one leader, got none
    --- FAIL: TestReElection3A (5.05s)
    ...

### The code

Implement Raft by adding code to raft1/raft.go. In that file you'll find skeleton code, plus examples of how to send and receive RPCs.

Your implementation must support the following interface. You'll find more details in comments in raft.go.

    // create a new Raft server instance:
    rf := Make(peers, me, persister, applyCh)

    // start agreement on a new log entry:
    rf.Start(command interface{}) (index, term, isleader)

    // ask a Raft for its current term, and whether it thinks it is leader
    rf.GetState() (term, isLeader)

    // each time a new entry is committed to the log, each Raft peer
    // should send an ApplyMsg to the service (or tester).
    type ApplyMsg

A service calls Make(peers,me,…) to create a Raft peer. The peers argument is an array of network identifiers of the Raft peers (including this one), for use with RPC. The me argument is the index of this peer in the peers array. Start(command) asks Raft to start the processing to append the command to the replicated log. Start() should return immediately, without waiting for the log appends to complete. The service expects your implementation to send an ApplyMsg for each newly committed log entry to the applyCh channel argument to Make().

raft.go contains example code that sends an RPC (sendRequestVote()) and that handles an incoming RPC (RequestVote()). Your Raft peers should exchange RPCs using the labrpc Go package (source in src/labrpc). The tester can tell labrpc to delay RPCs, re-order them, and discard them to simulate various network failures. While you can temporarily modify labrpc, make sure your Raft works with the original labrpc, since that's what we'll use to test and grade your lab. Your Raft instances must interact only with RPC; for example, they are not allowed to communicate using shared Go variables or files.

### Part A: Leader Election

Implement Raft leader election and heartbeats (AppendEntries RPCs with no log entries). The goal for Part A is for a single leader to be elected, for the leader to remain the leader if there are no failures, and for a new leader to take over if the old leader fails or if packets to/from the old leader are lost. Run go test -run 3A to test your Part A code.

#### Hints

- You can't easily run your Raft implementation directly; instead you should run it by way of the tester, i.e. go test -run 3A .
- Follow the paper's Figure 2. At this point you care about sending and receiving RequestVote RPCs, the Rules for Servers that relate to elections, and the State related to leader election,
- Add the Figure 2 state for leader election to the Raft struct in raft.go. You'll also need to define a struct to hold information about each log entry.
- Fill in the RequestVoteArgs and RequestVoteReply structs. Modify Make() to create a background goroutine that will kick off leader election periodically by sending out RequestVote RPCs when it hasn't heard from another peer for a while. Implement the RequestVote() RPC handler so that servers will vote for one another.
- To implement heartbeats, define an AppendEntries RPC struct (though you may not need all the arguments yet), and have the leader send them out periodically. Write an AppendEntries RPC handler method.
- The tester requires that the leader send heartbeat RPCs no more than ten times per second.
- The tester requires your Raft to elect a new leader within five seconds of the failure of the old leader (if a majority of peers can still communicate).
- The paper's Section 5.2 mentions election timeouts in the range of 150 to 300 milliseconds. Such a range only makes sense if the leader sends heartbeats considerably more often than once per 150 milliseconds (e.g., once per 10 milliseconds). Because the tester limits you tens of heartbeats per second, you will have to use an election timeout larger than the paper's 150 to 300 milliseconds, but not too large, because then you may fail to elect a leader within five seconds.
- You may find Go's [rand](https://golang.org/pkg/math/rand/) useful.
- You'll need to write code that takes actions periodically or after delays in time. The easiest way to do this is to create a goroutine with a loop that calls [time.Sleep()](https://golang.org/pkg/time/#Sleep); see the ticker() goroutine that Make() creates for this purpose. Don't use Go's time.Timer or time.Ticker, which are difficult to use correctly.
- If your code has trouble passing the tests, read the paper's Figure 2 again; the full logic for leader election is spread over multiple parts of the figure.
- Don't forget to implement GetState().
- The tester calls your Raft's rf.Kill() when it is permanently shutting down an instance. You can check whether Kill() has been called using rf.killed(). You may want to do this in all loops, to avoid having dead Raft instances print confusing messages.
- Go RPC sends only struct fields whose names start with capital letters. Sub-structures must also have capitalized field names (e.g. fields of log records in an array). The labgob package will warn you about this; don't ignore the warnings.
- The most challenging part of this lab may be the debugging. Spend some time making your implementation easy to debug.
- If you fail a test, the tester produces a file that visualizes a timeline with events marked along it, including network partitions, crashed servers, and checks performed. Further, you can add your own annotations by writing, for example, tester.Annotate("Server 0", "short description", "details").

Be sure you pass the 3A tests before continuing further so that you see something like this:

    go test -run 3A
    Test (3A): initial election (reliable network)...
    ... Passed --  time  3.0s #peers 3 #RPCs    38 #Ops    0
    Test (3A): election after network failure (reliable network)...
    ... Passed --  time  8.5s #peers 3 #RPCs    88 #Ops    0
      Test (3A): multiple elections (reliable network)...
    ... Passed --  time  5.4s #peers 7 #RPCs   290 #Ops    0
    PASS
    ok      cpsc416-2025w1/raft1    16.873s

Each "Passed" line contains several numbers; these are the time that the test took in seconds, the number of Raft peers, and the number of RPCs sent during the test. Your numbers will differ from those shown here. You can ignore the numbers if you like, but they may help you sanity-check the number of RPCs that your implementation sends. The grading script will fail your solution if it takes more than 600 seconds for all of the tests (go test), or if any individual test takes more than 120 seconds.

When we grade your submissions, we will run the tests without the [\-race flag](https://go.dev/blog/race-detector). However, you should make sure that your code consistently passes the tests with the \-race flag.

### Part B: Log Replication

Implement the leader and follower code to append new log entries, so that the go test -run 3B tests pass.

- Raft log is 1-indexed, but we suggest that you view it as 0-indexed, and starting out with an entry (at index=0) that has term 0. That allows the very first AppendEntries RPC to contain 0 as PrevLogIndex, and be a valid index into the log.
- Your first goal should be to pass TestBasicAgree3B(). Start by implementing Start(), then write the code to send and receive new log entries via AppendEntries RPCs, following Figure 2. Send each newly committed entry on applyCh on each peer.
- You will need to implement the election restriction (section 5.4.1 in the paper).
- Your code may have loops that repeatedly check for certain events. Don't have these loops execute continuously without pausing, since that will slow your implementation enough that it fails tests. Use Go's [condition variables](https://golang.org/pkg/sync/#Cond), or insert a time.Sleep(10 \* time.Millisecond) in each loop iteration.
- If you fail a test, look at raft_test.go and trace the test code from there to understand what's being tested.

The tests for upcoming labs may fail your code if it runs too slowly. You can check how much real time and CPU time your solution uses with the time command. Here's typical output:

    go test -run 3B
    Test (3B): basic agreement (reliable network)...
    ... Passed --   1.3  3    18    0
    Test (3B): RPC byte count (reliable network)...
    ... Passed --   2.8  3    56    0
    Test (3B): test progressive failure of followers (reliable network)...
    ... Passed --   5.3  3   188    0
    Test (3B): test failure of leaders (reliable network)...
    ... Passed --   6.4  3   378    0
    Test (3B): agreement after follower reconnects (reliable network)...
    ... Passed --   5.9  3   176    0
    Test (3B): no agreement if too many followers disconnect (reliable network)...
    ... Passed --   4.3  5   288    0
    Test (3B): concurrent Start()s (reliable network)...
    ... Passed --   1.5  3    32    0
    Test (3B): rejoin of partitioned leader (reliable network)...
    ... Passed --   5.3  3   216    0
    Test (3B): leader backs up quickly over incorrect follower logs (reliable network)...
    ... Passed --  12.1  5  1528    0
    Test (3B): RPC counts aren't too high (reliable network)...
    ... Passed --   3.1  3   106    0
    PASS
    ok      cpsc416-2025w1/raft1    48.353s

The "ok cpsc416-2025w1/raft 48.353s" means that Go measured the time taken for the Part B tests to be 48.353s seconds of real (wall-clock) time. If your solution uses much more than a minute for the Part B tests, you may run into trouble later on. Look for time spent sleeping or waiting for RPC timeouts, loops that run without sleeping or waiting for conditions or channel messages, or large numbers of RPCs sent.

### Part C: Persistence

If a Raft-based server reboots it should resume service where it left off. This requires that Raft keep persistent state that survives a reboot. The paper's Figure 2 mentions which state should be persistent.

A real implementation would write Raft's persistent state to disk each time it changed, and would read the state from disk when restarting after a reboot. Your implementation won't use the disk; instead, it will save and restore persistent state from a Persister object (see persister.go). Whoever calls Raft.Make() supplies a Persister that initially holds Raft's most recently persisted state (if any). Raft should initialize its state from that Persister, and should use it to save its persistent state each time the state changes. Use the Persister's ReadRaftState() and Save() methods.

Complete the functions persist() and readPersist() in raft.go by adding code to save and restore persistent state. You will need to encode (or "serialize") the state as an array of bytes in order to pass it to the Persister. Use the labgob encoder; see the comments in persist() and readPersist(). labgob is like Go's gob encoder but prints error messages if you try to encode structures with lower-case field names. For now, pass nil as the second argument to persister.Save(). Insert calls to persist() at the points where your implementation changes persistent state. Once you've done this, and if the rest of your implementation is correct, you should pass all of the Part C tests.

You will probably need the optimization that backs up nextIndex by more than one entry at a time. Look at the [extended Raft paper] starting at the bottom of page 7 and top of page 8 (marked by a gray line). The paper is vague about the details; you will need to fill in the gaps. One possibility is to have a rejection message include:

    XTerm:  term in the conflicting entry (if any)
    XIndex: index of first entry with that term (if any)
    XLen:   log length

Then the leader's logic can be something like:

    Case 1: leader doesn't have XTerm:
        nextIndex = XIndex
    Case 2: leader has XTerm:
        nextIndex = (index of leader's last entry for XTerm) + 1
    Case 3: follower's log is too short:
        nextIndex = XLen

#### Hints

- The Part C tests are more demanding than those for Part A or Part B, and failures may be caused by problems in your code for Part A or Part B.

Your code should pass all the Part C tests (as shown below), as well as the Part A and Part B tests.

    $ go test -run 3C
    Test (3C): basic persistence (reliable network)...
    ... Passed --   6.6  3   110    0
    Test (3C): more persistence (reliable network)...
    ... Passed --  15.6  5   428    0
    Test (3C): partitioned leader and one follower crash, leader restarts (reliable network)...
    ... Passed --   3.1  3    50    0
    Test (3C): Figure 8 (reliable network)...
    ... Passed --  33.7  5   654    0
    Test (3C): unreliable agreement (unreliable network)...
    ... Passed --   2.1  5  1076    0
    Test (3C): Figure 8 (unreliable) (unreliable network)...
    ... Passed --  31.9  5  4400    0
    Test (3C): churn (reliable network)...
    ... Passed --  16.8  5  4896    0
    Test (3C): unreliable churn (unreliable network)...
    ... Passed --  16.1  5  7204    0
    PASS
    ok      cpsc416-2025w1/raft1    126.054s

It is a good idea to run the tests multiple times and check that each run prints PASS.

    for i in {0..10}; do go test; done

### Part D: Log Compaction

As things stand now, a rebooting server replays the complete Raft log in order to restore its state. However, it's not practical for a long-running service to remember the complete Raft log forever. Instead, you'll modify Raft to cooperate with services that persistently store a "snapshot" of their state from time to time, at which point Raft discards log entries that precede the snapshot. The result is a smaller amount of persistent data and faster restart. However, it's now possible for a follower to fall so far behind that the leader has discarded the log entries it needs to catch up; the leader must then send a snapshot plus the log starting at the time of the snapshot. Section 7 of the [extended Raft paper](https://canvas.ubc.ca/files/41931584) outlines the scheme; you will have to design the details.

Your Raft must provide the following function that the service can call with a serialized snapshot of its state:

Snapshot(index int, snapshot \[\]byte)

In Part D, the tester calls Snapshot() periodically. The service layer calls Snapshot() on every peer (not just on the leader).

The index argument indicates the highest log entry that's reflected in the snapshot. Raft should discard its log entries before that point. You'll need to revise your Raft code to operate while storing only the tail of the log.

You'll need to implement the InstallSnapshot RPC discussed in the paper that allows a Raft leader to tell a lagging Raft peer to replace its state with a snapshot. You will likely need to think through how InstallSnapshot should interact with the state and rules in Figure 2.

When a follower's Raft code receives an InstallSnapshot RPC, it can use the applyCh to send the snapshot to the service in an ApplyMsg. The ApplyMsg struct definition already contains the fields you will need (and which the tester expects). Take care that these snapshots only advance the service's state, and don't cause it to move backwards.

If a server crashes, it must restart from persisted data. Your Raft should persist both Raft state and the corresponding snapshot. Use the second argument to persister.Save() to save the snapshot. If there's no snapshot, pass nil as the second argument.

When a server restarts, the application layer reads the persisted snapshot and restores its saved state.

Implement Snapshot() and the InstallSnapshot RPC, as well as the changes to Raft to support these (e.g, operation with a trimmed log). Your solution is complete when it passes the Part D tests (and all the previous Lab 3 tests).

#### Hints

- A good place to start is to modify your code to so that it is able to store just the part of the log starting at some index X. Initially you can set X to zero and run the Part B/C tests. Then make Snapshot(index) discard the log before index, and set X equal to index. If all goes well you should now pass the first Part D test.
- A common reason for failing the first Part D test is that followers take too long to catch up to the leader.
- Next: have the leader send an InstallSnapshot RPC if it doesn't have the log entries required to bring a follower up to date.
- Send the entire snapshot in a single InstallSnapshot RPC. Don't implement Figure 13's offset mechanism for splitting up the snapshot.
- Raft must discard old log entries in a way that allows the Go garbage collector to free and re-use the memory; this requires that there be no reachable references (pointers) to the discarded log entries.
- A reasonable amount of time to consume for the full set of Lab 3 tests (3A+3B+3C+3D) without \-race is 6 minutes of real time and one minute of CPU time. When running with \-race, it is about 10 minutes of real time and two minutes of CPU time.

Your code should pass all the Part D tests (as shown below), as well as the Part A, Part B, and Part C tests.

    go test -run 3D
    Test (3D): snapshots basic (reliable network)...
    ... Passed --   3.3  3   522    0
    Test (3D): install snapshots (disconnect) (reliable network)...
    ... Passed --  48.4  3  2710    0
    Test (3D): install snapshots (disconnect) (unreliable network)...
    ... Passed --  56.1  3  3025    0
    Test (3D): install snapshots (crash) (reliable network)...
    ... Passed --  33.3  3  1559    0
    Test (3D): install snapshots (crash) (unreliable network)...
    ... Passed --  38.1  3  1723    0
    Test (3D): crash and restart all servers (unreliable network)...
    ... Passed --  11.2  3   296    0
    Test (3D): snapshot initialization after crash (unreliable network)...
    ... Passed --   4.3  3    84    0
    PASS
    ok      cpsc416-2025w1/raft1    195.006s

## Submission Instructions

Add an INFO.md file inside the `raft1` directory. If you used GenAI for this
lab, cite it (i.e., name the tool) and annotate it (i.e., briefly explain how
you used it) in this file. For example, "We asked ChatGPT for a simple
explanation of Go's RPC mechanism." or "We asked Cursor to generate (from
specifications in the prompt) all the arguments and reply structs in the
raft.go file."

In the INFO.md file, also list all the team members and how you split up the
tasks. If you were pair (group) programming all the time, say so.

We expect you to only modify the `src/raft1/raft.go` file.
You may add other files in the directory, such as `src/raft1/debug.go`,
but DO NOT modify any other existing file in a way that is critical for your
solution to work, as we will be replacing those files with their pristine
versions.

Since Canvas group submissions are not working as expected, this time I am
asking you to add student IDs in the name of the submission. Please strictly
adhere to the following format. To submit your solution, first compress the
`raft1` directory to create the `raft_studentid1_studentid2_studentid3.tar.gz`
file. For example, on Linux or macOS, if you are inside the `src/raft1/`
directory, you can run the following commands.

```bash
cd ..
tar -czvf raft_studentid1_studentid2_studentid3.tar.gz raft1/
```

If your team consists of two students or just one student, then rename the
file accoridngly, e.g., `raft_studentid1_studentid2.tar.gz` or
`raft_studentid1.tar.gz`.

Example submission names: `raft_11111111.tar.gz`, `raft_11111111_22222222.tar.gz`, `raft_11111111_22222222_33333333.tar.gz`.

This time, you may skip the group sign-up on Canvas, as it is not quite
working as intended. Simply have one of the team members submit the `*.tar.gz`
file when ready.
