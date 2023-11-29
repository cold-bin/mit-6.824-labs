## lab2A

### 论文回顾

> 去除原论文中不是lab2A描述

Raft 是一种用来管理章节 2 中描述的复制日志的算法。图 2 为了参考之用，总结这个算法的简略版本，图 3列举了这个算法的一些关键特性。图中的这些元素会在剩下的章节逐一介绍。

Raft 通过选举一个杰出的领导人，然后给予他全部的管理复制日志的责任来实现一致性。领导人从客户端接收日志条目（log
entries），把日志条目复制到其他服务器上，并告诉其他的服务器什么时候可以安全地将日志条目应用到他们的状态机中。拥有一个领导人大大简化了对复制日志的管理。例如，领导人可以决定新的日志条目需要放在日志中的什么位置而不需要和其他服务器商议，并且数据都从领导人流向其他服务器。一个领导人可能会发生故障，或者和其他服务器失去连接，在这种情况下一个新的领导人会被选举出来。

通过领导人的方式，Raft 将一致性问题分解成了三个相对独立的子问题，这些问题会在接下来的子章节中进行讨论：

* **领导选举**：当现存的领导人发生故障的时候, 一个新的领导人需要被选举出来（章节 5.2）
* **日志复制**：领导人必须从客户端接收日志条目（log entries）然后复制到集群中的其他节点，并强制要求其他节点的日志和自己保持一致。
* **安全性**：在 Raft 中安全性的关键是在图 3 中展示的状态机安全：如果有任何的服务器节点已经应用了一个确定的日志条目到它的状态机中，那么其他服务器节点不能在同一个日志索引位置应用一个不同的指令。章节
  5.4 阐述了 Raft 算法是如何保证这个特性的；这个解决方案涉及到选举机制（5.2 节）上的一个额外限制。

**状态**：

所有服务器上的持久性状态
(在响应 RPC 请求之前，已经更新到了稳定的存储设备)

| 参数          | 解释                                           |
|-------------|----------------------------------------------|
| currentTerm | 服务器已知最新的任期（在服务器首次启动时初始化为0，单调递增）              |
| votedFor    | 当前任期内收到选票的 candidateId，如果没有投给任何候选人 则为空       |
| log[]       | 日志条目；每个条目包含了用于状态机的命令，以及领导人接收到该条目时的任期（初始索引为1） |

所有服务器上的易失性状态

| 参数          | 解释                               |
|-------------|----------------------------------|
| commitIndex | 已知已提交的最高的日志条目的索引（初始值为0，单调递增）     |
| lastApplied | 已经被应用到状态机的最高的日志条目的索引（初始值为0，单调递增） |

领导人（服务器）上的易失性状态
(选举后已经重新初始化)

| 参数           | 解释                                               |
|--------------|--------------------------------------------------|
| nextIndex[]  | 对于每一台服务器，发送到该服务器的下一个日志条目的索引（初始值为领导人最后的日志条目的索引+1） |
| matchIndex[] | 对于每一台服务器，已知的已经复制到该服务器的最高日志条目的索引（初始值为0，单调递增）      |

**追加条目（AppendEntries）RPC**：

由领导人调用，用于日志条目的复制，同时也被当做心跳使用

| 参数           | 解释                                                                             |
|--------------|--------------------------------------------------------------------------------|
| term         | 领导人的任期                                                                         |
| leaderId     | 领导人 ID 因此跟随者可以对客户端进行重定向（译者注：跟随者根据领导人 ID 把客户端的请求重定向到领导人，比如有时客户端把请求发给了跟随者而不是领导人） |
| prevLogIndex | 紧邻新日志条目之前的那个日志条目的索引                                                            |
| prevLogTerm  | 紧邻新日志条目之前的那个日志条目的任期                                                            |
| entries[]    | 需要被保存的日志条目（被当做心跳使用时，则日志条目内容为空；为了提高效率可能一次性发送多个）                                 |
| leaderCommit | 领导人的已知已提交的最高的日志条目的索引                                                           |

| 返回值     | 解释                                                    |
|---------|-------------------------------------------------------|
| term    | 当前任期，对于领导人而言 它会更新自己的任期                                |
| success | 如果跟随者所含有的条目和 prevLogIndex 以及 prevLogTerm 匹配上了，则为 true |

接收者的实现：

1. 返回假 如果领导人的任期小于接收者的当前任期（译者注：这里的接收者是指跟随者或者候选人）（5.1 节）

**请求投票（RequestVote）RPC**：

由候选人负责调用用来征集选票（5.2 节）

| 参数           | 解释             |
|--------------|----------------|
| term         | 候选人的任期号        |
| candidateId  | 请求选票的候选人的 ID   |
| lastLogIndex | 候选人的最后日志条目的索引值 |
| lastLogTerm  | 候选人最后日志条目的任期号  |

| 返回值         | 解释                    |
|-------------|-----------------------|
| term        | 当前任期号，以便于候选人去更新自己的任期号 |
| voteGranted | 候选人赢得了此张选票时为真         |

接收者实现：

1. 如果`term < currentTerm`返回 false （5.2 节）
2. 如果 votedFor 为空或者为 candidateId，并且候选人的日志至少和自己一样新，那么就投票给他（5.2 节，5.4 节）

**所有服务器需遵守的规则**：

所有服务器：

* 如果接收到的 RPC 请求或响应中，任期号`T > currentTerm`，则令 `currentTerm = T`，并切换为跟随者状态（5.1 节）

跟随者（5.2 节）：

* 响应来自候选人和领导人的请求
* 如果在超过选举超时时间的情况之前没有收到**当前领导人**（即该领导人的任期需与这个跟随者的当前任期相同）的心跳/附加日志，或者是给某个候选人投了票，就自己变成候选人

候选人（5.2 节）：

* 在转变成候选人后就立即开始选举过程

    * 自增当前的任期号（currentTerm）
    * 给自己投票
    * 重置选举超时计时器
* 发送请求投票的 RPC 给其他所有服务器（并发发送，如果没有响应就不管）
* 如果接收到大多数服务器的选票，那么就变成领导人
* 如果接收到来自新的领导人的附加日志（AppendEntries）RPC，则转变成跟随者
* 如果选举过程超时，则再次发起一轮选举

领导人：

* 一旦成为领导人：发送空的附加日志（AppendEntries）RPC（心跳）给其他所有的服务器；在一定的空余时间之后不停的重复发送，以防止跟随者超时（5.2
  节）

### 实现思路

Lab2A只需要实现心跳、请求投票、定时任务。

#### 心跳

- 领导人任期内需要向其他所有对等点（候选者和跟随者）发送`AppendEntries`RPC以重置选举超时器，维护自己的权威，以防止其他节点进入选举。
- 除了任期内，领导人需要周期性地对所有对等点广播心跳以外，在领导人被选举出来的一刻也应该对所有对等点发送广播（这里就不等待定时任务到来才进行广播了，可能存在一定的时延导致某些节点无法收到，从而延长多次选举周期）

**实现**

- 接受者（可能是跟随者，也可能是候选者）收到心跳后，首先判断是否可以接收请求，如果可以更换为跟随者并刷新选举超时计时器。

  ```go
  
  // rf是跟随者或者候选人
  func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
  	rf.mu.Lock()
  	defer rf.mu.Unlock()
  	defer func() {
  		Debug(dInfo, "after called AppendEntries S%d status{currentTerm:%d,role:%s,log:%v,lastApplied:%d,commitIndex:%d}",
  			rf.me, rf.currentTerm, rf.role.String(), rf.log, rf.lastApplied, rf.commitIndex)
  	}()
  	Debug(dInfo, "before called AppendEntries S%d status{currentTerm:%d,role:%s,log:%v,lastApplied:%d,commitIndex:%d}",
  		rf.me, rf.currentTerm, rf.role.String(), rf.log, rf.lastApplied, rf.commitIndex)
  
  	if args.Term < rf.currentTerm { /*请求的leader任期落后了，leader会变成follower，应该拒绝请求*/
  		reply.Term, reply.Success = rf.currentTerm, false
  		return
  	}
  
  	// 刷新选举超时器
  	rf.changeRole(follower)
  	rf.electionTimer.Reset(withRandomElectionDuration())
  	Debug(dInfo, "S%d -> S%d heartbeat, S%d reset election timer", args.LeaderId, rf.me, rf.me)
  }
  ```

- 请求者异步并发发送请求RPC即可

  > 论文中提到：如果对等点没有响应，那就一直请求直到响应，但是这在某些情况可能会造成内存泄漏，例如集群中某个follower一直处于下线状态，那么这段时期的所有RPC都存在并且随着任期增加，而GC赶不上内存分配的速度，那么可能会存在内存泄漏的问题
  >
  > 
  >
  > 可以酌情考虑配置一定的重试次数，超过次数过后报警

  ```go
  func (rf *Raft) heartbeatBroadcast() {
  	Debug(dInfo, "S%d start heartbeat broadcast", rf.me)
  
  	n := len(rf.peers)
  	for i := 0; i < n; i++ {
  		if i == rf.me { /*skip self*/
  			continue
  		}
  
  		args := &AppendEntriesArgs{
  			Term:         rf.currentTerm,
  			LeaderId:     rf.me,
  			PrevLogIndex: len(rf.log) - 1,
  			PrevLogTerm:  rf.log[len(rf.log)-1].Term,
  			LeaderCommit: rf.commitIndex,
  		}
  
  		// 异步发送
  		go func(peer int) {
  			reply := &AppendEntriesReply{}
  			if ok := rf.sendAppendEntries(peer, args, reply); !ok {
  				return
  			}
  
  			rf.mu.Lock()
  			defer rf.mu.Unlock()
  
  			if reply.Term > rf.currentTerm {
  				rf.currentTerm = reply.Term
  				rf.changeRole(follower)
  			}
  		}(i)
  	}
  }
  ```

#### 请求投票

成为候选者，就会广播投票请求

> 如果是广播到候选者时，不会收到选票，因为i候选者已经给自己投票了

- 接收者

  1. 如果接受者任期较大时，请求无效
  2. 如果接收者任期较小时，接收者切换为follower，并清除自己的上一任期的投票结果
  3. 如果候选人的日志至少和自己一样新，那么就投票

  ```go
  func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
  	// Your code here (2A, 2B).
  	rf.mu.Lock()
  	defer rf.mu.Unlock()
  	defer func() {
  		Debug(dInfo, "after called RequestVote, S%d status{votedFor:%d,role:%s,currentTerm:%d}",
  			rf.me, rf.votedFor, rf.role.String(), rf.currentTerm)
  	}()
  	Debug(dInfo, "before called RequestVote, S%d status{votedFor:%d,role:%s,currentTerm:%d}",
  		rf.me, rf.votedFor, rf.role.String(), rf.currentTerm)
  
  	if args.Term < rf.currentTerm { /*请求者任期较小，拒绝请求*/
  		reply.Term, reply.VoteGranted = rf.currentTerm, false
  		return
  	}
  
  	if args.Term > rf.currentTerm { /*还可以投票*/
  		rf.changeRole(follower)
  		rf.currentTerm, rf.votedFor = args.Term, noVote
  	}
  
  	if (rf.votedFor == noVote || rf.votedFor == args.CandidateId) && rf.logUpToDate(args.LastLogTerm, args.LastLogIndex) { /*日志至少和自己一样新，才能投票，否则不能投票*/
  		rf.votedFor = args.CandidateId
  		rf.electionTimer.Reset(withRandomElectionDuration())
  		Debug(dInfo, "S%d vote to S%d, S%d election timer reset", rf.me, args.CandidateId, rf.me)
  		reply.Term, reply.VoteGranted = rf.currentTerm, true
  		return
  	}
  
  	reply.Term, reply.VoteGranted = rf.currentTerm, false
  }
  ```

  

- 请求者

  开始选举时，就会并发广播请求投票RPC。如果有候选者成为leader，就需要**立即**广播领导人的心跳，避免其他对等点的选举超时器超时，触发新一轮选举。

  ```go
  // for candidate
  func (rf *Raft) startElection() {
  	rf.currentTerm++
  	rf.votedFor = rf.me
  	approvedNum := 1 // 先给自己投一票
  	rf.electionTimer.Reset(withRandomElectionDuration())
  	Debug(dInfo, "S%d start election, S%d reset election timer", rf.me, rf.me)
  	n := len(rf.peers)
  
  	args := &RequestVoteArgs{
  		Term:         rf.currentTerm,
  		CandidateId:  rf.me,
  		LastLogIndex: len(rf.log) - 1,
  		LastLogTerm:  0,
  	}
  
  	if len(rf.log) > 0 {
  		args.LastLogTerm = rf.log[len(rf.log)-1].Term
  	}
  
  	// 向其他对等点并发发送投票请求
  	for i := 0; i < n; i++ {
  		if i == rf.me { /*skip self*/
  			continue
  		}
  
  		go func(peer int) {
  			reply := new(RequestVoteReply)
  			ok := false
  			for !ok { /* network is destroyed*/
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
  						rf.heartbeatBroadcast()
  						rf.initializeLeaderEasilyLostState() /*领导人（服务器）上的易失性状态(选举后已经重新初始化)*/
  					}
  				}
  			}
  		}(i)
  	}
  }
  ```

#### 定时任务

lab2A中只有两个定时任务：

- 一个是选举超时器超时时，需要开启新一轮选举
- 另一个则是领导人每隔一段时间就需要向集群中的其他对等点广播心跳以维护权威

```go
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
				Debug(dInfo, "S%d reset election timer", rf.me)
			}

			rf.mu.Unlock()
		}
	}
}
```

#### 调试过程

实现的初版能过lab2A的测试，但是会有`warning: term changed even though there were no failures`的问题。意思是，没有任何网络分区、延时等错误，但是在测试程序休眠2秒后，任期会发生变化。

- 通过日志打印，我发现最开始的时候，心跳RPC只发送了一次过后就没有了，所以跟随者没有收到后续心跳RPC，从而导致跟随者频繁地选举超时并开启下一任期的选举。

  解决：将原代码实现的`Timer`换成了`Ticker`，这样就不用考虑`Timer`的刷新了，因为心跳是固定时间段

- 但是，问题依然还没有被解决，依然会出现上诉WARNING。我又查看了一下日志，发现选举的leader经过多次心跳过后，leader的选举超时器会发生超时。原来如此，我应该在leader发送完广播之后，也要重置自己的选举计时器。

### 结果

```bash
➜  raft git:(main) VERBOSE=0 go test -race -run 2A
Test (2A): initial election ...
  Passed -- real time: 3.1       number of Raft peers:3          number of RPC sends:  60        number of bytes:  15050         number of Raft agreements reported:   0
Test (2A): election after network failure ...
  Passed -- real time: 4.6       number of Raft peers:3          number of RPC sends: 139        number of bytes:  26141         number of Raft agreements reported:   0
Test (2A): multiple elections ...
  Passed -- real time: 5.5       number of Raft peers:7          number of RPC sends: 675        number of bytes: 123169         number of Raft agreements reported:   0
PASS
ok      6.5840/raft     14.281s
```

