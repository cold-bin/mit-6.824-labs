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
	"6.5840/labgob"
	"bytes"
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
	CommandTerm  int

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

const (
	noVote   = -1
	NoLeader = -1
)

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
	role   role
	leader int // for lab3

	applyMsg chan ApplyMsg // 已提交日志需要被应用到状态机里
	cond     *sync.Cond

	// persistent states in every machine
	currentTerm       int    // 服务器已知最新的任期（在服务器首次启动时初始化为0，单调递增）
	votedFor          int    // 当前任期内收到选票的 CandidateId，如果没有投给任何候选人 则为空
	log               []Logt // 日志条目；每个条目包含了用于状态机的命令，以及领导人接收到该条目时的任期（初始索引为1）
	lastIncludedIndex int    // 快照的最后一个日志条目索引

	snapshot []byte // 总是保存最新的快照

	// states of abnormal loss in every machine
	commitIndex int // 已知已提交的最高的日志条目的索引（初始值为0，单调递增）
	lastApplied int // 已经被应用到状态机的最高的日志条目的索引（初始值为0，单调递增）

	// states of abnormal loss only in leader machine
	nextIndex  []int // 对于每一台服务器，发送到该服务器的下一个日志条目的索引（初始值为领导人最后的日志条目的索引+1）
	matchIndex []int // 对于每一台服务器，已知的已经复制到该服务器的最高日志条目的索引（初始值为0，单调递增），作用就是ack掉成功调用日志复制的rpc

	electionTimer   *time.Timer   // random timer
	heartbeatTicker *time.Ticker  // leader每隔至少100ms一次
	replicateSignal chan struct{} // 新增的 leader log快速replicate的信号
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

func (rf *Raft) firstLogIndex() int {
	if rf.lastIncludedIndex < 1 { /*没有快照*/
		return rf.lastIncludedIndex
	}
	return rf.lastIncludedIndex + 1 /*有快照*/
}

func (rf *Raft) lastLogIndex() int {
	// 快照：{nil 1 2 3 4 5 6 7 8 9} {nil,1}
	// lastIndex: 9+2-1=10
	// 无快照：{nil 1 2 3 4 5 6 7 8 9}
	// lastIndex: 10-1=9
	return rf.lastIncludedIndex + len(rf.log) - 1
}

func (rf *Raft) lastLogTerm() int {
	if len(rf.log) < 2 /*只能从快照拿最后日志的term*/ {
		return rf.log[0].Term
	}
	return rf.log[len(rf.log)-1].Term
}

// 真实index在log中的索引
func (rf *Raft) logIndex(realIndex int) int {
	// 快照：{nil 1 2 3 4 5 6 7 8 9} {nil,10,11}
	// logIndex: 10-9 = 1
	// 无快照：{nil 1 2 3 4 5 6 7 8 9 10}
	// logIndex: 10-0=10
	return realIndex - rf.lastIncludedIndex
}

// log中的索引在log和快照里的索引
func (rf *Raft) realIndex(logIndex int) int {
	// 快照：{nil 1 2 3 4 5 6 7 8 9} {nil,10,11}
	// realIndex: 9+1 = 10
	// 无快照：{nil 1 2 3 4 5 6 7 8 9 10}
	// realIndex: 0+1=1
	return rf.lastIncludedIndex + logIndex
}

func (rf *Raft) realLogLen() int {
	// 快照：{nil 1 2 3 4 5 6 7 8 9} {nil,10,11}
	// realLogLen: 9+3=12
	// 无快照：{nil 1 2 3 4 5 6 7 8 9}
	// realLogLen: 0+10
	return rf.lastIncludedIndex + len(rf.log)
}

func (rf *Raft) Role() string {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.role.String()
}

// 选举后，领导人的易失状态需要重新初始化
func (rf *Raft) initializeLeaderEasilyLostState() {
	defer func() {
		Debug(dLeader, "after S%d initializeLeaderEasilyLostState,nextIndex:%v,matchIndex:%v", rf.me, rf.nextIndex, rf.matchIndex)
	}()

	for i := 0; i < len(rf.peers); i++ {
		rf.nextIndex[i] = rf.realLogLen()
		rf.matchIndex[i] = rf.lastIncludedIndex
	}

	// 领导人的nextIndex和matchIndex是确定的
	rf.matchIndex[rf.me] = rf.lastLogIndex()
	rf.nextIndex[rf.me] = rf.lastLogIndex() + 1
}

func (rf *Raft) changeRole(r role) {
	preRole := rf.role
	rf.role = r
	Debug(dInfo, "S%d role %s->%s", rf.me, preRole.String(), r.String())
}

// Leader 获取leader, 没有就返回 NoLeader
func (rf *Raft) Leader() int {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.leader
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

// PersistentStatus 持久化状态
type PersistentStatus struct {
	Log               []Logt
	CurrentTerm       int
	VotedFor          int
	LastIncludedIndex int
	LastIncludedTerm  int
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
	defer func() {
		Debug(dPersist, "S%d persisted status{currentTerm:%d,lastIncludedTerm:%d,lastIncludedIndex:%d,log_len:%d,commitIndex:%d,appliedIndex:%d}",
			rf.me, rf.currentTerm, rf.log[0].Term, rf.lastIncludedIndex, len(rf.log)-1, rf.commitIndex, rf.lastApplied)
	}()

	rf.persister.Save(rf.PersistStatusBytes(), rf.snapshot)
}

func (rf *Raft) PersistStatusBytes() []byte {
	status := &PersistentStatus{
		Log:               rf.log,
		CurrentTerm:       rf.currentTerm,
		VotedFor:          rf.votedFor,
		LastIncludedIndex: rf.lastIncludedIndex,
		LastIncludedTerm:  rf.log[0].Term,
	}

	w := new(bytes.Buffer)
	if err := labgob.NewEncoder(w).Encode(status); err != nil {
		Debug(dError, "encode err:%v", err)
		return nil
	}

	return w.Bytes()
}

// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	defer func() {
		Debug(dPersist, "after read persist, S%d recover to status{currentTerm:%d,commitIndex:%d,applied:%d,lastIncludedIndex:%d,log_len:%d}",
			rf.me, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.lastIncludedIndex, len(rf.log)-1)
	}()

	// Your code here (2C).
	persistentStatus := &PersistentStatus{}
	if err := labgob.NewDecoder(bytes.NewBuffer(data)).Decode(persistentStatus); err != nil {
		Debug(dError, "readPersist decode err:%v", err)
		return
	}

	// 裁减剩下的log
	rf.log = persistentStatus.Log
	rf.currentTerm = persistentStatus.CurrentTerm
	rf.votedFor = persistentStatus.VotedFor
	// 最新的快照点
	rf.lastIncludedIndex = persistentStatus.LastIncludedIndex
	rf.log[0].Term = persistentStatus.LastIncludedTerm
	// 之前被快照的数据，一定是被applied
	rf.commitIndex = persistentStatus.LastIncludedIndex
	rf.lastApplied = persistentStatus.LastIncludedIndex
	// 加载上一次的快照
	rf.snapshot = rf.persister.ReadSnapshot()
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
// 指的是，创建了快照（log[:index]），那么raft可以裁减掉被快照过的日志
// 这里的快照是从 applyMsg 传进来的，这里需要将日志放入persist里，但是仅仅只是定期快照。
// leader发送给其他follower的快照安装，需要手动调用此函数
// 注意：快照的日志已经把第一个日志（nil）也快照了的
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer func() {
		Debug(dSnap, "after Snapshot, S%d status{currentTerm:%d,commitIndex:%d,applied:%d,snapshotIndex:%d,lastIncludedIndex:%d,log_len:%d}",
			rf.me, rf.currentTerm, rf.commitIndex, rf.lastApplied, index, rf.lastIncludedIndex, len(rf.log)-1)
	}()

	if rf.lastApplied < index /*快照点超过应用点无效,必须等待日志被应用过后才能对其快照，防止应用日志前被裁减了*/ ||
		rf.lastIncludedIndex >= index /*快照点如果小于前一次快照点，没有必要快照*/ {
		return
	}

	defer rf.persist()
	// 丢弃被快照了的日志，同时修改其他状态
	// last: snap{nil,1,2,3} {nil}
	// now:  snap{nil,1,2,3,4,5} {nil,4,5}
	split := rf.logIndex(index)
	rf.lastIncludedIndex = index
	rf.log = append([]Logt{{Term: rf.log[split].Term}}, rf.log[split+1:]...)
	rf.snapshot = snapshot
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

	if args.Term < rf.currentTerm { /*请求者任期较小，拒绝请求*/
		reply.Term, reply.VoteGranted = rf.currentTerm, false
		return
	}

	defer rf.persist()

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

	XTerm  int // term in the conflicting entry (if any)
	XIndex int // index of first entry with that term (if any)
	XLen   int // log length
}

// rf是跟随者或者候选人
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer func() {
		Debug(dLog, "after AppendEntries,S%d -> S%d status{currentTerm:%d,role:%s,commitIndex:%d,applied:%d,lastIncludedIndex:%d,log_len:%d}",
			rf.leader, rf.me, rf.currentTerm, rf.role.String(), rf.commitIndex, rf.lastApplied, rf.lastIncludedIndex, len(rf.log)-1)
	}()

	if args.Term < rf.currentTerm { /*请求的leader任期落后了，leader会变成follower，应该拒绝请求*/
		reply.Term, reply.Success = rf.currentTerm, false
		return
	}

	defer rf.persist()

	// 刷新选举超时器
	rf.changeRole(follower)
	rf.electionTimer.Reset(withRandomElectionDuration())

	if args.Term > rf.currentTerm { /*请求的leader任期更大，那么rf的任期需要更新，并转化为follower,并且取消以前任期的无效投票*/
		rf.currentTerm, rf.votedFor = args.Term, noVote
	}
	rf.leader = args.LeaderId

	if rf.lastIncludedIndex > args.PrevLogIndex /*对等点的快照点已经超过本次日志复制的点，没有必要接受此日志复制rpc了*/ {
		reply.Term, reply.Success = rf.currentTerm, false
		return
	}

	if rf.lastLogIndex() < args.PrevLogIndex /*可能rf过期，领导者已经应用了很多日志*/ {
		//这种情况下，该raft实例断网一段时间过后，日志落后。所以直接返回 XLen即可。
		//leader更新nextIndex为XLen即可，表示当前raft实例缺少XLen及后面的日志，leader在下次广播时带上这些日志
		// leader   0{0} 1{101 102 103} 5{104}	PrevLogIndex=3	nextIndex=4
		// follower 0{0} 1{101 102 103} 5{104}  PrevLogIndex=3  nextIndex=4
		// follower 0{0} 1{101} 5 				PrevLogIndex=1  nextIndex=1
		reply.XTerm, reply.XIndex, reply.XLen = -1, -1, rf.realLogLen()
		reply.Term, reply.Success = rf.currentTerm, false
		return
	}

	/*冲突：该条目的任期在 prevLogIndex，上不能和 prevLogTerm 匹配上，则返回假*/
	index := rf.logIndex(args.PrevLogIndex)
	if rf.log[index].Term != args.PrevLogTerm {
		// 从后往前找冲突条目，返回最小冲突条目的索引
		conflictIndex, conflictTerm := -1, rf.log[index].Term
		for i := args.PrevLogIndex; i > rf.commitIndex; i-- {
			if rf.log[rf.logIndex(i)].Term != conflictTerm {
				break
			}
			conflictIndex = i
		}

		reply.XTerm, reply.XIndex, reply.XLen = conflictTerm, conflictIndex, rf.realLogLen()
		reply.Term, reply.Success = rf.currentTerm, false
		return
	}

	for i, entry := range args.Entries {
		index = rf.logIndex(args.PrevLogIndex + 1 + i)
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
		rf.commitIndex = min(args.LeaderCommit, rf.lastLogIndex())
		rf.cond.Signal()
	}

	reply.Term, reply.Success = rf.currentTerm, true
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

// 不分片
type InstallSnapshotArgs struct {
	Term              int    // 领导人的任期号
	LeaderId          int    // 领导人的 ID，以便于跟随者重定向请求
	LastIncludedIndex int    // 快照中包含的最后日志条目的索引值
	LastIncludedTerm  int    // 快照中包含的最后日志条目的任期号
	Data              []byte // 从偏移量开始的快照分块的原始字节
}

type InstallSnapshotReply struct {
	Term int // 当前任期号（currentTerm），便于领导人更新自己
}

func (rf *Raft) InstallSnapshot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer func() {
		Debug(dSnap, "after InstallSnapshot,S%d -> S%d status{currentTerm:%d,commitIndex:%d,applied:%d,lastIncludedIndex:%d,log_len:%d}",
			rf.leader, rf.me, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.lastIncludedIndex, len(rf.log)-1)
	}()

	//1. 如果`term < currentTerm`就立即回复
	if args.Term < rf.currentTerm /*请求的领导者过期了，不能安装过期leader的快照*/ {
		reply.Term = rf.currentTerm
		return
	}

	if args.Term > rf.currentTerm /*当前raft落后，可以接着安装快照*/ {
		rf.currentTerm, rf.votedFor = args.Term, noVote
	}
	rf.leader = args.LeaderId

	rf.changeRole(follower)
	rf.electionTimer.Reset(withRandomElectionDuration())

	//5. 保存快照文件，丢弃具有较小索引的任何现有或部分快照
	if args.LastIncludedIndex <= rf.lastIncludedIndex /*raft快照点要先于leader时，无需快照*/ {
		reply.Term = rf.currentTerm
		return
	}

	if args.LastIncludedIndex <= rf.commitIndex /*leader快照点小于当前提交点*/ {
		reply.Term = rf.currentTerm
		return
	}

	defer rf.persist()

	rf.lastIncludedIndex = args.LastIncludedIndex
	rf.log[0].Term = args.LastIncludedTerm
	rf.commitIndex = args.LastIncludedIndex
	rf.lastApplied = args.LastIncludedIndex
	rf.snapshot = args.Data
	msg := ApplyMsg{
		SnapshotValid: true,
		Snapshot:      args.Data,
		SnapshotTerm:  args.LastIncludedTerm,
		SnapshotIndex: args.LastIncludedIndex,
	}
	//6. 如果现存的日志条目与快照中最后包含的日志条目具有相同的索引值和任期号，则保留其后的日志条目并进行回复
	for i := 1; i < len(rf.log); i++ {
		if rf.realIndex(i) == args.LastIncludedIndex && rf.log[i].Term == args.LastIncludedTerm {
			rf.log = append([]Logt{{Term: args.LastIncludedTerm}}, rf.log[i+1:]...)
			go func() {
				rf.applyMsg <- msg
			}()

			reply.Term = rf.currentTerm
			return
		}
	}
	//7. 丢弃整个日志（因为整个log都是过期的）
	rf.log = []Logt{{Term: args.LastIncludedTerm}}
	//8. 使用快照重置状态机（并加载快照的集群配置）
	go func() {
		rf.applyMsg <- msg
	}()

	reply.Term = rf.currentTerm
	return
}

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
	rf.persist()
	rf.matchIndex[rf.me], rf.nextIndex[rf.me] = rf.lastLogIndex(), rf.realLogLen()
	index, term, isLeader = rf.lastLogIndex(), rf.currentTerm, true

	// 快速唤醒log replicate
	go func() {
		rf.replicateSignal <- struct{}{}
	}()

	Debug(dLog, "S%d Start cmd:%v,index:%d", rf.me, command, index)
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

func (rf *Raft) electionEvent() {
	for rf.killed() == false {
		select {
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			rf.changeRole(candidate)
			rf.startElection()
			rf.mu.Unlock()
		}
	}
}

func (rf *Raft) heartbeatEvent() {
	for rf.killed() == false {
		select {
		case <-rf.heartbeatTicker.C:
			rf.mu.Lock()
			if rf.role == leader {
				rf.heartbeatBroadcast()
				rf.electionTimer.Reset(withRandomElectionDuration()) // leader广播完毕时，也应该把自己的选举超时器刷新一下
			}
			rf.mu.Unlock()
		}
	}
}

func (rf *Raft) logReplicateEvent() {
	for rf.killed() == false {
		select {
		case <-rf.replicateSignal:
			rf.mu.Lock()
			if rf.role == leader {
				rf.heartbeatBroadcast()
				rf.electionTimer.Reset(withRandomElectionDuration())
			}
			rf.mu.Unlock()
		}
	}
}

// 将已提交的日志应用到状态机里。
// 注意：防止日志被应用状态机之前被裁减掉，也就是说，一定要等日志被应用过后才能被裁减掉。
func (rf *Raft) applierEvent() {
	for rf.killed() == false {
		rf.mu.Lock()
		for rf.lastApplied >= rf.commitIndex {
			rf.cond.Wait()
		}
		msgs := make([]ApplyMsg, 0, rf.commitIndex-rf.lastApplied)
		for i := rf.lastApplied + 1; i <= rf.commitIndex; i++ {
			msgs = append(msgs, ApplyMsg{
				CommandValid: true,
				Command:      rf.log[rf.logIndex(i)].Command,
				CommandIndex: i,
				CommandTerm:  rf.log[rf.logIndex(i)].Term,
			})
			rf.lastApplied++
		}
		rf.mu.Unlock()

		for _, msg := range msgs {
			rf.applyMsg <- msg
		}
	}
}

func (rf *Raft) heartbeatBroadcast() {
	Debug(dTimer, "S%d start broadcast", rf.me)

	n := len(rf.peers)
	for peer := 0; peer < n; peer++ {
		if peer == rf.me {
			continue
		}

		Debug(dLog, "heartbeatBroadcast S%d{lastIncludedIndex:%d} -> S%d{nextIndex:%d}",
			rf.me, rf.lastIncludedIndex, peer, rf.nextIndex[peer])

		if rf.nextIndex[peer] <= rf.lastIncludedIndex /*存在于快照中，发送安装快照RPC*/ {
			args := &InstallSnapshotArgs{
				Term:              rf.currentTerm,
				LeaderId:          rf.me,
				LastIncludedIndex: rf.lastIncludedIndex,
				LastIncludedTerm:  rf.log[0].Term,
				Data:              rf.snapshot,
			}

			Debug(dLog, `sendInstallSnapshot S%d -> S%d, LastIncludedIndex:%d,LastIncludedTerm:%d`,
				rf.me, peer, args.LastIncludedIndex, args.LastIncludedTerm)

			go rf.handleSendInstallSnapshot(peer, args)
		} else /*存在于未裁减的log中，发起日志复制rpc*/ {
			args := &AppendEntriesArgs{
				Term:         rf.currentTerm,
				LeaderId:     rf.me,
				Entries:      make([]Logt, 0),
				PrevLogIndex: rf.nextIndex[peer] - 1,
				LeaderCommit: rf.commitIndex,
			}
			if args.PrevLogIndex > rf.lastIncludedIndex &&
				args.PrevLogIndex < rf.lastIncludedIndex+len(rf.log) /*下一个日志在leader log里，且前一个日志没在快照里，也在leader log里*/ {
				args.PrevLogTerm = rf.log[rf.logIndex(args.PrevLogIndex)].Term
			} else if args.PrevLogIndex == rf.lastIncludedIndex /*下一个日志在leader log里，但上一个日志在快照里，没在leader log里*/ {
				//args.PrevLogIndex = rf.lastIncludedIndex
				args.PrevLogTerm = rf.log[0].Term
			}
			//deep copy
			args.Entries = append(args.Entries, rf.log[rf.logIndex(rf.nextIndex[peer]):]...)

			Debug(dLog, `sendAppendEntries S%d -> S%d, lastIncludedIndex:%d args{PrevLogIndex:%d,PrevLogTerm:%d,LeaderCommit:%d,log_entries_len:%d"}`,
				rf.me, peer, rf.lastIncludedIndex, args.PrevLogIndex, args.PrevLogTerm, args.LeaderCommit, len(args.Entries))

			go rf.handleSendAppendEntries(peer, args)
		}
	}
}

func (rf *Raft) handleSendAppendEntries(peer int, args *AppendEntriesArgs) {
	reply := &AppendEntriesReply{}
	if ok := rf.sendAppendEntries(peer, args, reply); ok {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		defer func() {
			Debug(dLog, `after sendAppendEntries S%d, nextIndex:%d matchIndex:%d`, peer, rf.nextIndex[peer], rf.matchIndex[peer])
		}()

		if rf.currentTerm != args.Term || rf.role != leader { /*不是leader，没有必要在进行广播*/
			return
		}

		if reply.Term > rf.currentTerm { /*过期该返回*/
			rf.changeRole(follower)
			rf.leader = NoLeader
			rf.currentTerm = reply.Term
			rf.persist()
			return
		}

		if reply.Success { /*心跳成功或日志复制成功*/
			rf.matchIndex[peer] = args.PrevLogIndex + len(args.Entries)
			rf.nextIndex[peer] = args.PrevLogIndex + len(args.Entries) + 1
			/*超过半数节点追加成功，也就是已提交，并且还是leader，那么就可以应用当前任期里的日志到状态机里。找到共识N：遍历对等点，找到相同的N*/
			rf.checkAndCommitLogs()
		} else {
			// 快速定位nextIndex
			rf.findNextIndex(peer, reply)
		}
	}
}

func (rf *Raft) handleSendInstallSnapshot(peer int, args *InstallSnapshotArgs) {
	reply := &InstallSnapshotReply{}
	if ok := rf.sendInstallSnapshot(peer, args, reply); ok {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		defer func() {
			Debug(dLog, `after sendInstallSnapshot S%d status{nextIndex:%d,matchIndex:%d,LastIncludedIndex:%d,LastIncludedTerm:%d}`,
				peer, rf.nextIndex[peer], rf.matchIndex[peer], rf.lastIncludedIndex, rf.log[0].Term)
		}()

		if rf.currentTerm != args.Term || rf.role != leader {
			return
		}

		if reply.Term > rf.currentTerm {
			rf.changeRole(follower)
			rf.leader = NoLeader
			rf.currentTerm = reply.Term
			rf.persist()
			return
		}

		rf.nextIndex[peer] = args.LastIncludedIndex + 1
		rf.matchIndex[peer] = args.LastIncludedIndex
	}
}

func (rf *Raft) checkAndCommitLogs() {
	n := len(rf.peers)
	N := rf.commitIndex
	for _N := rf.commitIndex + 1; _N < rf.lastIncludedIndex+len(rf.log); _N++ {
		succeedNum := 0
		for peer := 0; peer < n; peer++ {
			if _N <= rf.matchIndex[peer] && rf.log[_N-rf.lastIncludedIndex].Term == rf.currentTerm {
				succeedNum++
			}
		}
		if succeedNum > n/2 { /*继续找更大的共识N*/
			N = _N
		}
	}

	if N > rf.commitIndex { /*leader可以提交了*/
		Debug(dLog, `S%d commit to index: %d, and lastIncludedIndex:%d`, rf.me, N, rf.lastIncludedIndex)
		rf.commitIndex = N
		rf.cond.Signal()
	}
}

func (rf *Raft) findNextIndex(peer int, reply *AppendEntriesReply) {
	if reply.XTerm == -1 && reply.XIndex == -1 { /*Case 3: follower's log is too short*/
		rf.nextIndex[peer] = reply.XLen
		return
	}

	ok := false
	for i, entry := range rf.log { /*Case 2: leader has XTerm*/
		if entry.Term == reply.XTerm {
			ok = true
			rf.nextIndex[peer] = rf.lastIncludedIndex + i
		}
	}

	if !ok { /*Case 1: leader doesn't have XTerm*/
		rf.nextIndex[peer] = reply.XIndex
	}
}

// for candidate
func (rf *Raft) startElection() {
	rf.currentTerm++
	rf.votedFor = rf.me
	rf.leader = NoLeader
	rf.persist()
	approvedNum := 1 // 先给自己投一票
	rf.electionTimer.Reset(withRandomElectionDuration())
	Debug(dTimer, "S%d start election, S%d reset election timer", rf.me, rf.me)
	n := len(rf.peers)

	// 向其他对等点并发发送投票请求
	for i := 0; i < n; i++ {
		if i == rf.me {
			continue
		}
		args := &RequestVoteArgs{
			Term:         rf.currentTerm,
			CandidateId:  rf.me,
			LastLogIndex: rf.lastLogIndex(),
			LastLogTerm:  rf.lastLogTerm(),
		}
		go func(peer int) {
			reply := new(RequestVoteReply)
			if ok := rf.sendRequestVote(peer, args, reply); !ok {
				return
			}

			rf.mu.Lock()
			defer rf.mu.Unlock()

			if reply.Term > rf.currentTerm {
				rf.currentTerm, rf.votedFor = reply.Term, noVote
				rf.persist()
				rf.changeRole(follower)
			} else if reply.Term == rf.currentTerm && rf.role == candidate /*我们需要确认此刻仍然是candidate，没有发生状态变化*/ {
				if reply.VoteGranted {
					approvedNum++
					if approvedNum > n/2 { /*找到leader了，需要及时广播，防止选举超时*/
						rf.changeRole(leader)
						rf.leader = rf.me
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
		peers:     peers,
		persister: persister,
		me:        me,
		dead:      0,
		role:      follower,
		leader:    NoLeader,
		applyMsg:  applyCh,
		//cond:              sync.NewCond(&sync.Mutex{}),
		currentTerm:       0,
		votedFor:          noVote,
		log:               make([]Logt, 1),
		lastIncludedIndex: 0,
		commitIndex:       0,
		lastApplied:       0,
		nextIndex:         make([]int, n),
		matchIndex:        make([]int, n),
		electionTimer:     time.NewTimer(withRandomElectionDuration()),
		heartbeatTicker:   time.NewTicker(withStableHeartbeatDuration()),
		replicateSignal:   make(chan struct{}),
	}
	// Your initialization code here (2A, 2B, 2C).
	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())
	rf.cond = sync.NewCond(&rf.mu)

	// 注册事件驱动并监听
	go rf.electionEvent()     // 选举协程
	go rf.heartbeatEvent()    // 心跳协程
	go rf.logReplicateEvent() // replicate协程
	go rf.applierEvent()      // apply协程

	return rf
}
