package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	//	"bytes"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	//	"6.5840/labgob"
	"6.5840/labrpc"
)

// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 2D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int

	// For 2D:
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

type role uint8

const (
	leader role = iota + 1
	follower
	candidate
)

func (r role) String() string {
	switch r {
	case follower:
		return "follower"
	case candidate:
		return "candidate"
	case leader:
		return "leader"
	default:
		return "NO NAME"
	}
}

const noVote = -1

type Logt struct {
	Command interface{}
	Term    int
}

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.
	role role

	applyMsg chan ApplyMsg // 已提交日志需要被应用到状态机里
	conds    []*sync.Cond
	// persistent states in every machine
	currentTerm int    // 服务器已知最新的任期（在服务器首次启动时初始化为0，单调递增）
	votedFor    int    // 当前任期内收到选票的 CandidateId，如果没有投给任何候选人 则为空
	log         []Logt // 日志条目；每个条目包含了用于状态机的命令，以及领导人接收到该条目时的任期（初始索引为1）

	// states of abnormal loss in every machine
	commitIndex int // 已知已提交的最高的日志条目的索引（初始值为0，单调递增）
	lastApplied int // 已经被应用到状态机的最高的日志条目的索引（初始值为0，单调递增）

	// states of abnormal loss only in leader machine
	nextIndex  []int // 对于每一台服务器，发送到该服务器的下一个日志条目的索引（初始值为领导人最后的日志条目的索引+1）
	matchIndex []int // 对于每一台服务器，已知的已经复制到该服务器的最高日志条目的索引（初始值为0，单调递增），作用就是ack掉成功调用日志复制的rpc

	electionTimer   *time.Timer  // random timer
	heartbeatTicker *time.Ticker // leader每隔至少100ms一次
}

func withRandomElectionDuration() time.Duration {
	return time.Duration(300+(rand.Int63()%300)) * time.Millisecond
}

func withStableHeartbeatDuration() time.Duration {
	return 100 * time.Millisecond
}

// 论文里安全性的保证：参数的日志是否至少和自己一样新
func (rf *Raft) isLogUpToDate(lastLogTerm, lastLogIndex int) bool {
	return lastLogTerm > rf.lastLogTerm() || (lastLogTerm == rf.lastLogTerm() && lastLogIndex >= rf.lastLogIndex())
}

func (rf *Raft) lastLogIndex() int {
	return len(rf.log) - 1
}

func (rf *Raft) lastLogTerm() int {
	if len(rf.log) < 1 {
		return rf.log[0].Term
	}
	return rf.log[len(rf.log)-1].Term
}

// 选举后，领导人的易失状态需要重新初始化
func (rf *Raft) initializeLeaderEasilyLostState() {
	defer func() {
		Debug(dLeader, "after S%d initializeLeaderEasilyLostState,nextIndex:%v,matchIndex:%v", rf.me, rf.nextIndex, rf.matchIndex)
	}()
	Debug(dLeader, "before S%d initializeLeaderEasilyLostState,nextIndex:%v,matchIndex:%v", rf.me, rf.nextIndex, rf.matchIndex)
	for i := 0; i < len(rf.peers); i++ {
		rf.nextIndex[i] = len(rf.log)
		rf.matchIndex[i] = 0
	}
	// 领导人的nextIndex和matchIndex是确定的
	rf.matchIndex[rf.me] = len(rf.log) - 1
	rf.nextIndex[rf.me] = len(rf.log)
}

func (rf *Raft) changeRole(r role) {
	preRole := rf.role
	rf.role = r
	Debug(dInfo, "S%d role %s->%s", rf.me, preRole.String(), r.String())
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	var (
		term     int
		isleader bool
	)
	// Your code here (2A).
	rf.mu.Lock()
	term, isleader = rf.currentTerm, rf.role == leader
	rf.mu.Unlock()

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
	// Your code here (2C).
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
	// Your code here (2C).
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

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).

}

type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term         int // 候选人的任期号
	CandidateId  int // 请求选票的候选人的 ID
	LastLogIndex int // 候选人的最后日志条目的索引值
	LastLogTerm  int // 候选人最后日志条目的任期号
}

type RequestVoteReply struct {
	// Your data here (2A).
	Term        int  // 当前任期号，以便于候选人去更新自己的任期号
	VoteGranted bool // 候选人赢得了此张选票时为真
}

func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer func() {
		Debug(dVote, "after called RequestVote, S%d status{votedFor:%d,role:%s,currentTerm:%d}",
			rf.me, rf.votedFor, rf.role.String(), rf.currentTerm)
	}()
	Debug(dVote, "before called RequestVote, S%d status{votedFor:%d,role:%s,currentTerm:%d}",
		rf.me, rf.votedFor, rf.role.String(), rf.currentTerm)

	if args.Term < rf.currentTerm { /*请求者任期较小，拒绝请求*/
		reply.Term, reply.VoteGranted = rf.currentTerm, false
		return
	}

	if args.Term > rf.currentTerm { /*还可以投票*/
		rf.changeRole(follower)
		rf.currentTerm, rf.votedFor = args.Term, noVote
	}

	if (rf.votedFor == noVote || rf.votedFor == args.CandidateId) &&
		rf.isLogUpToDate(args.LastLogTerm, args.LastLogIndex) { /*日志至少和自己一样新，才能投票，否则不能投票*/
		rf.votedFor = args.CandidateId
		rf.electionTimer.Reset(withRandomElectionDuration())
		Debug(dVote, "S%d vote to S%d, S%d election timer reset", rf.me, args.CandidateId, rf.me)
		reply.Term, reply.VoteGranted = rf.currentTerm, true
		return
	}

	reply.Term, reply.VoteGranted = rf.currentTerm, false
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

type AppendEntriesArgs struct {
	Term         int    // 领导人的任期
	LeaderId     int    // 领导人 ID 因此跟随者可以对客户端进行重定向（译者注：跟随者根据领导人 ID 把客户端的请求重定向到领导人，比如有时客户端把请求发给了跟随者而不是领导人）
	PrevLogIndex int    // 紧邻新日志条目之前的那个日志条目的索引 (一致性检查)
	PrevLogTerm  int    // 紧邻新日志条目之前的那个日志条目的任期（一致性检查）
	Entries      []Logt // 需要被保存的日志条目（被当做心跳使用时，则日志条目内容为空；为了提高效率可能一次性发送多个）
	LeaderCommit int    // 领导人的已知已提交的最高的日志条目的索引
}

type AppendEntriesReply struct {
	Term    int  // 当前任期，对于领导人而言 它会更新自己的任期
	Success bool // 如果跟随者所含有的条目和 PrevLogIndex 以及 PrevLogTerm 匹配上了，则为 true
}

// rf是跟随者或者候选人
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer func() {
		Debug(dInfo, "after AppendEntries S%d status{currentTerm:%d,role:%s,log:%v,lastApplied:%d,commitIndex:%d,leaderCommit:%d}",
			rf.me, rf.currentTerm, rf.role.String(), rf.log, rf.lastApplied, rf.commitIndex, args.LeaderCommit)
	}()
	Debug(dInfo, "before AppendEntries S%d status{currentTerm:%d,role:%s,log:%v,lastApplied:%d,commitIndex:%d,leaderCommit:%d}",
		rf.me, rf.currentTerm, rf.role.String(), rf.log, rf.lastApplied, rf.commitIndex, args.LeaderCommit)

	if args.Term < rf.currentTerm { /*请求的leader任期落后了，leader会变成follower，应该拒绝请求*/
		reply.Term, reply.Success = rf.currentTerm, false
		return
	}

	// 刷新选举超时器
	rf.changeRole(follower)
	rf.electionTimer.Reset(withRandomElectionDuration())
	Debug(dTimer, "S%d -> S%d AppendEntries, S%d reset election timer", args.LeaderId, rf.me, rf.me)

	if args.Term > rf.currentTerm { /*请求的leader任期更大，那么rf的任期需要更新，并转化为follower,并且取消以前任期的无效投票*/
		rf.currentTerm, rf.votedFor = args.Term, noVote
	}

	if len(rf.log) <= args.PrevLogIndex /*可能rf过期，领导者已经应用了很多日志*/ ||
		rf.log[args.PrevLogIndex].Term != args.PrevLogTerm /*该条目的任期在 prevLogIndex 上不能和 prevLogTerm 匹配上，则返回假*/ {
		reply.Term, reply.Success = rf.currentTerm, false
		return
	}

	for i, entry := range args.Entries {
		index := args.PrevLogIndex + i + 1
		if index < len(rf.log) { /*重叠*/
			if rf.log[index].Term != entry.Term { /*看是否发生冲突*/
				rf.log = rf.log[:index]        // 删除当前以及后续所有log
				rf.log = append(rf.log, entry) // 把新log加入进来
			}
			/*没有冲突，那么就不添加这个重复的log*/
		} else if index == len(rf.log) { /*没有重叠，且刚好在下一个位置*/
			rf.log = append(rf.log, entry)
		}
	}

	if args.LeaderCommit > rf.commitIndex {
		rf.commitIndex = min(args.LeaderCommit, len(rf.log)-1)
		rf.conds[rf.me].Signal()
	}
	reply.Term, reply.Success = rf.currentTerm, true
}

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
// 一致性协议由心跳开启，这样就不用区分心跳和日志复制了
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := false

	// Your code here (2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.role != leader {
		return -1, -1, false
	}

	/* leader追加日志，长度一定大于等于2 */
	rf.log = append(rf.log, Logt{
		Command: command,
		Term:    rf.currentTerm,
	})
	rf.matchIndex[rf.me], rf.nextIndex[rf.me] = len(rf.log)-1, len(rf.log)
	index, term, isLeader = len(rf.log)-1, rf.currentTerm, true

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

func (rf *Raft) ticker() {
	for rf.killed() == false {
		// Your code here (2A)
		// Check if a leader election should be started.
		select {
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			rf.changeRole(candidate)
			rf.startElection()
			rf.mu.Unlock()
		case <-rf.heartbeatTicker.C:
			rf.mu.Lock()
			if rf.role == leader {
				rf.heartbeatBroadcast()
				rf.electionTimer.Reset(withRandomElectionDuration()) // leader广播完毕时，也应该把自己的选举超时器刷新一下
				Debug(dTimer, "S%d reset election timer", rf.me)
			}
			rf.mu.Unlock()
		}
	}
}

// 将已提交的日志应用到状态机里
func (rf *Raft) applier() {
	for rf.killed() == false {
		rf.conds[rf.me].L.Lock()
		rf.conds[rf.me].Wait()
		rf.conds[rf.me].L.Unlock()

		rf.mu.Lock()
		for i := rf.lastApplied + 1; i <= rf.commitIndex && i < len(rf.log); i++ {
			rf.applyMsg <- ApplyMsg{
				CommandValid: true,
				Command:      rf.log[i].Command,
				CommandIndex: i,
			}
			rf.lastApplied++
			Debug(dLog2, "applied, S%d applier status{lastApplied:%d,commitIndex:%d}", rf.me, rf.lastApplied, rf.commitIndex)
		}
		rf.mu.Unlock()
	}
}

func (rf *Raft) heartbeatBroadcast() {
	Debug(dTimer, "S%d start broadcast", rf.me)
	n := len(rf.peers)
	for peer := 0; peer < n; peer++ {
		if peer == rf.me {
			continue
		}

		args := &AppendEntriesArgs{
			Term:         rf.currentTerm,
			LeaderId:     rf.me,
			Entries:      make([]Logt, 0),
			PrevLogIndex: rf.nextIndex[peer] - 1,
			PrevLogTerm:  rf.log[rf.nextIndex[peer]-1].Term,
			LeaderCommit: rf.commitIndex,
		}
		args.Entries = append(args.Entries, rf.log[rf.nextIndex[peer]:]...)

		go func(peer int) {
			reply := &AppendEntriesReply{}
			Debug(dLog, `sendAppendEntries S%d -> S%d, args %+v`, rf.me, peer, args)
			if ok := rf.sendAppendEntries(peer, args, reply); ok {
				rf.mu.Lock()
				defer rf.mu.Unlock()
				defer func() {
					Debug(dLog, `after sendAppendEntries S%d, nextIndex:%d matchIndex:%d`, peer, rf.nextIndex[peer], rf.matchIndex[peer])
				}()
				Debug(dLog, `before sendAppendEntries S%d, nextIndex:%d matchIndex:%d`, peer, rf.nextIndex[peer], rf.matchIndex[peer])

				if rf.role != leader { /*不是leader，没有必要在进行广播*/
					return
				}

				if reply.Term > rf.currentTerm { /*过期该返回*/
					rf.changeRole(follower)
					rf.currentTerm = reply.Term
					return
				}

				if reply.Success { /*心跳成功或日志复制成功*/
					rf.matchIndex[peer] = args.PrevLogIndex + len(args.Entries)
					rf.nextIndex[peer] = rf.matchIndex[peer] + 1

					/*超过半数节点追加成功，也就是已提交，并且还是leader，那么就可以应用当前任期里的日志到状态机里。找到共识N：遍历对等点，找到相同的N*/
					N := rf.commitIndex
					for _N := rf.commitIndex + 1; _N < len(rf.log); _N++ {
						succeedNum := 0
						for peer := 0; peer < n; peer++ {
							if _N <= rf.matchIndex[peer] && rf.log[_N].Term == rf.currentTerm {
								succeedNum++
							}
						}
						if succeedNum > n/2 { /*继续找更大的共识N*/
							N = _N
						}
					}

					if N > rf.commitIndex { /*leader可以提交了*/
						Debug(dLog, `S%d commit to index: %d`, rf.me, N)
						rf.commitIndex = N
						rf.conds[rf.me].Signal()
					}
				} else { /*失败，减小nextIndex重试*/
					rf.nextIndex[peer]--
					if rf.nextIndex[peer] < 1 {
						rf.nextIndex[peer] = 1
					}
				}
			}
		}(peer)
	}
}

// for candidate
func (rf *Raft) startElection() {
	rf.currentTerm++
	rf.votedFor = rf.me
	approvedNum := 1 // 先给自己投一票
	rf.electionTimer.Reset(withRandomElectionDuration())
	Debug(dTimer, "S%d start election, S%d reset election timer", rf.me, rf.me)
	n := len(rf.peers)

	// 向其他对等点并发发送投票请求
	for i := 0; i < n; i++ {
		if i == rf.me {
			continue
		}

		go func(peer int) {
			reply := new(RequestVoteReply)
			ok := false
			for !rf.killed() && !ok {
				rf.mu.Lock()
				args := &RequestVoteArgs{
					Term:         rf.currentTerm,
					CandidateId:  rf.me,
					LastLogIndex: len(rf.log) - 1,
					LastLogTerm:  0,
				}
				if len(rf.log) > 0 {
					args.LastLogTerm = rf.log[len(rf.log)-1].Term
				}
				rf.mu.Unlock()

				ok = rf.sendRequestVote(peer, args, reply)
			}

			rf.mu.Lock()
			defer rf.mu.Unlock()

			if reply.Term > rf.currentTerm {
				rf.currentTerm, rf.votedFor = reply.Term, noVote
				rf.changeRole(follower)
			} else if reply.Term == rf.currentTerm && rf.role == candidate /*我们需要确认此刻仍然是candidate，没有发生状态变化*/ {
				if reply.VoteGranted {
					approvedNum++
					if approvedNum > n/2 { /*找到leader了，需要及时广播，防止选举超时*/
						rf.changeRole(leader)
						rf.initializeLeaderEasilyLostState() /*领导人（服务器）上的易失性状态(选举后已经重新初始化)*/
						rf.heartbeatBroadcast()
					}
				}
			}
		}(i)
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
// applyCh 的作用是提供一个机制，使得 Raft 算法可以将已提交的日志条目的应用结果传递给上层应用程序，以实现状态机的复制和一致性。
func Make(peers []*labrpc.ClientEnd, me int, persister *Persister, applyCh chan ApplyMsg) *Raft {
	n := len(peers)
	rf := &Raft{
		peers:           peers,
		persister:       persister,
		me:              me,
		dead:            0,
		role:            follower,
		applyMsg:        applyCh,
		conds:           make([]*sync.Cond, n),
		currentTerm:     0,
		votedFor:        noVote,
		log:             make([]Logt, 1),
		commitIndex:     0,
		lastApplied:     0,
		nextIndex:       make([]int, n),
		matchIndex:      make([]int, n),
		electionTimer:   time.NewTimer(withRandomElectionDuration()),
		heartbeatTicker: time.NewTicker(withStableHeartbeatDuration()),
	}
	// Your initialization code here (2A, 2B, 2C).
	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())
	for i := 0; i < n; i++ {
		rf.nextIndex[i] = len(rf.log)
		rf.matchIndex[i] = 0
		rf.conds[i] = sync.NewCond(&sync.Mutex{})
	}

	go rf.ticker()
	go rf.applier()

	return rf
}
