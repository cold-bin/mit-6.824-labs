## lab2D

> 到现在实现的最艰难的一个lab

### 论文总结

直接看论文, 论文算是精华了.

### 实现思路

lab2D中需要我们实现的东西如下：

#### 实现`Snapshot()`方法

- 注意点1：快照点不能超过应用点

  该方法由应用层调用，应用层来决定何时对节点进行快照，而测试脚本中是每隔10条日志就进行一次快照。快照时需要注意，在lab2D前面的实现中，是把已提交和已应用的两个阶段通过条件变量分开的，中间这个间隙可能会被快照然后裁减掉未应用未提交甚至已提交的日志，这样可能会少了一些日志。为了保证在快照时，论文中的“已提交的日志一定会被应用到状态机”的特性，在快照时需要判断当前快照点是否超过了应用点，如果没有超过，说明可以快照；如果超过了应用点，就不能裁减log，防止前面提到的问题发生。

- 注意点2：如果当前快照点小于等于上一次快照点，没有必要快照了

- 注意点3：持久化的过程中，需要保证最新的快照和最新的raft持久化状态，一起持久化，保证原子性.

  这点在`persist()`方法的注释中有提到。为此我给raft添加了`snapshot`字段用来表示raft持有的最新快照，调用`persist()`方法的时候，将快照一并持久化，从而保证原子性。

```go
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

```

#### 实现`InstallSnapshot()`方法

这个方法的时候，论文已经说的很明白了，实现起来问题不大。只是有些coner case可能在实现的时候会与论文说的有差别（这种只有debug了）。lab2D的要求我们去除安装快照的分片实现，只需要单次发送就可以了。这里总结一下实现步骤：

1. 如果`term < currentTerm`就立即回复（过期leader请求没必要处理）

2. 创建一个新的快照

3. 保存快照文件，丢弃具有较小索引的任何现有或部分快照

   > 这句话的意思是: 比较对等点的快照和leader发过来的安装快照. 要丢弃较小的快照点, 保留最大的快照点.

4. 如果现存的日志条目与快照中最后包含的日志条目具有相同的索引值和任期号，则保留其后的日志条目并进行回复

   > 这句话的意思是: 可能包含了leader的安装快照之后的新的状态变化，我们需要保留这些, 并且return.（中文翻译的bug...qwq，不注意看可能会理解错误）

5. 丢弃整个日志 

   > 如果在第4步没有return的话, 说明现存的日志条目与快照中最后不包含的日志条目具有相同的索引值和任期号, 也就是说, 当前log是过期的. 没有必要留存,直接删除掉

6. 使用快照重置状态机（并加载快照的集群配置）

除了论文和lab tips中的实现点以外，还有一些小的coner case需要注意：

- leader安装快照的过程请求了对等点, 算是一次ping/pong, 可以刷新选举计时器以及重新置为follower
- 如果对等点任期落后, 那么依然可以继续后面的步骤, 但是需要重置旧任期的选票和更新任期
- 使用Copy-on-Write的技术优化

```go
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
		Debug(dSnap, "after InstallSnapshot, S%d status{currentTerm:%d,commitIndex:%d,applied:%d,lastIncludedIndex:%d,log_len:%d}",
			rf.me, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.lastIncludedIndex, len(rf.log)-1)
	}()

	//1. 如果`term < currentTerm`就立即回复
	if args.Term < rf.currentTerm /*请求的领导者过期了，不能安装过期leader的快照*/ {
		reply.Term = rf.currentTerm
		return
	}

	if args.Term > rf.currentTerm /*当前raft落后，可以接着安装快照*/ {
		rf.currentTerm, rf.votedFor = args.Term, noVote
	}

	rf.changeRole(follower)
	rf.electionTimer.Reset(withRandomElectionDuration())

	//5. 保存快照文件，丢弃具有较小索引的任何现有或部分快照
	if args.LastIncludedIndex <= rf.lastIncludedIndex /*raft快照点要先于leader时，无需快照*/ {
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
```

##### 调用时机

论文中说的很明确了: 有可能follower处理得太慢或者新加入集群, 由于前面的日志被快照了, 那么leader就无法在log中找到要发送给follower的日志了, 只能发送快照过去. 

lab2D中给了一个tips: Even when the log is trimmed, your implemention still needs to properly send the term and index of the entry prior to new entries in `AppendEntries` RPCs; this may require saving and referencing the latest snapshot's `lastIncludedTerm/lastIncludedIndex` (consider whether this should be persisted).

发送日志复制RPC的请求参数中的`LastIncludedIndex`和`LastIncludedTerm`在快照机制出现过后, 可能存在这样一种边界情况. 下面是心跳逻辑中处理leader该发送日志复制RPC还是快照安装RPC的代码逻辑:

```go
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

```

#### 修改`readPersist()`方法

主要是为了实现前面日志复制RPC可能需要最新快照的最后一个条目的任期以及索引, 所以就持久化了两个新的raft状态变量`lastIncludedIndex`和`lastIncludedTerm`. 而且前面提到, 我在实现快照的时候, 是将被应用后的日志快照, 所以如果raft实例重新启动的话, 应用点和提交点应该也是从`lastIncludedIndex`开始.

```go
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
```



### 调试过程

#### 调试`test 1`

我把初版代码写完的时候，所有测试用例都没有过（偶尔会有一次过`test 1`）。后面改了日志打印信息，主要减少了一些不要信息的输出，例如对于日志我们不关心具体是什么，只关心复制到哪了，所以只需要打印相关索引信息即可。

当然，后面也封装了对下标的操作，这样问题也好排查，代码也更好修改。然后，在日志中看到了：当集群所有节点的日志全部快照了，也就是说快照裁减的日志没有剩下的特殊情况，nextIndex出现了回退现象。

```bash
034063 LOG1 before sendAppendEntries S0, nextIndex:190 matchIndex:189
034064 LOG1 after sendAppendEntries S0, nextIndex:189 matchIndex:189
034071 LOG1 before sendAppendEntries S1, nextIndex:190 matchIndex:189
034072 LOG1 after sendAppendEntries S1, nextIndex:189 matchIndex:189
```

非常令人匪夷所思的bug，然后我在前面的日志中发现，leader发送日志复制rpc，但是follower并没有复制成功，至此问题定位到了：应该是有个地方的边界情况没有考虑到。下面是关键代码，优化nextIndex定位的部分逻辑。

```go
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
```

因为集群中所有节点的情况一致，也就是`PrevLogIndex`和`lastIncludedIndex`的值是一样的，
所以就会出现`rf.log[0].Term != args.PrevLogTerm`的情况，进而日志复制失败，nextIndex回退的问题。

修复的方式有两种：一种是额外处理相等情况；还有一种方法就是，每次快照的时候初始化`rf.log[0].Term`为`lastIncludedTerm`即可。我采用的是第二种。采用第二种方案就不需要raft中暂存`lastIncludedTerm`了


#### 调试`test 2`

经过好几天的debug，终于把前面的`test 1`测试通过，但是在`test 2`的时候，总是遇到另一个比较奇怪的问题。

```bash
1: log map[0:<nil> 1:5926928346377738245 2:5899418714642564734 3:2523416114670145493 4:2645763998966155741 5:8669075945120996169 6:7089836692553293889 7:4329587389170088629 8:2003073101149869281 9:3121819971269749612]; 
server map[1:5926928346377738245 2:5899418714642564734 3:2523416114670145493 4:2645763998966155741 5:8669075945120996169 6:7089836692553293889 7:4329587389170088629 8:2003073101149869281 9:3121819971269749612 10:651793884186508261 11:6297140580147973579 12:7722938942633659376 13:4482676451686048456 14:2481212667518606188]
apply error: commit index=10 server=1 5926928346377738245 != server=2 651793884186508261
```

在上面的报错中显示日志`S1`的在`index=10`处的日志应该是`651793884186508261`。那么显然错误应该是出现在对`rf.log`的写操作中。经过排查与打印日志，最终发现问题出现在这里：

```go
logIndex := rf.logIndex(args.LastIncludedIndex)
if rf.log[logIndex].Term == args.LastIncludedTerm {
    fmt.Println("S", rf.me, "安装快照前：", rf.log)
    rf.log = append([]Logt{{Term: args.LastIncludedTerm}}, rf.log[logIndex+1:]...)
    fmt.Println("S", rf.me, "安装快照后：", rf.log)
    go func() {
        rf.applyMsg <- msg
    }()
    reply.Term = rf.currentTerm
    return
}
```

上面这段代码中。当`args.LastIncludedIndex`等于`rf.lastIncludedIndex`，也就是当前对等点和leader拥有相同的快照点时，下面的代码裁减会出现问题。会把第一条日志留下来。而且实现的语义并没有完全遵从论文。下面是代码修改，主要是直接跳过`index=0`这个不算现存日志的判断。

```go
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
```

#### 调试`data race`

lab2D使用`-race`测试的时候，偶尔出现下面的问题：

```bash
Test (2D): install snapshots (disconnect) ...
==================
WARNING: DATA RACE
Write at 0x00c0000f7520 by goroutine 37929:
  6.5840/raft.(*Raft).InstallSnapshot()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:544 +0x548
  runtime.call32()
      /usr/local/go/src/runtime/asm_amd64.s:748 +0x42
  reflect.Value.Call()
      /usr/local/go/src/reflect/value.go:380 +0xb5
  6.5840/labrpc.(*Service).dispatch()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:494 +0x484
  6.5840/labrpc.(*Server).dispatch()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:418 +0x24e
  6.5840/labrpc.(*Network).processReq.func1()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:240 +0x9c

Previous read at 0x00c0000f7520 by goroutine 37928:
  6.5840/raft.(*Raft).handleSendAppendEntries()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:742 +0x15d
  6.5840/raft.(*Raft).heartbeatBroadcast.func2()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:735 +0x4f

Goroutine 37929 (running) created at:
  6.5840/labrpc.(*Network).processReq()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:239 +0x28a
  6.5840/labrpc.MakeNetwork.func1.1()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:157 +0x9c

Goroutine 37928 (running) created at:
  6.5840/raft.(*Raft).heartbeatBroadcast()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:735 +0xd65
  6.5840/raft.(*Raft).ticker()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:654 +0x1ab
  6.5840/raft.Make.func1()
      /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:932 +0x33
==================
--- FAIL: TestSnapshotInstall2D (76.80s)
    testing.go:1465: race detected during execution of test
```

比较令我困惑的是尽管已经对`rf.lastIncludedIndex = args.LastIncludedIndex`的代码加了锁，依然会出现data race的问题。报错显示的原因是，读写冲突了，看代码的`742`行和`735`行，发现是开启协程时，打印日志的时候忘记加锁了。。。加上就好了。

#### 调试`test 5`

```bash
Test (2D): install snapshots (unreliable+crash) ...
panic: runtime error: index out of range [-7]

goroutine 46139 [running]:
6.5840/raft.(*Raft).AppendEntries(0xc0001c32c0, 0xc00037fb80, 0xc0001e2f60)
        /home/cold-bin/CodeProject/Go Code/6.5840/src/raft/raft.go:459 +0x5e9
reflect.Value.call({0xc0000d8550?, 0xc0003e5c50?, 0xc000213b18?}, {0x645f7e, 0x4}, {0xc000213c70, 0x3, 0xc000213b48?})
        /usr/local/go/src/reflect/value.go:596 +0xce7
reflect.Value.Call({0xc0000d8550?, 0xc0003e5c50?, 0xc0002c3598?}, {0xc000213c70?, 0xc0004e24e8?, 0xa1bbc2b4?})
        /usr/local/go/src/reflect/value.go:380 +0xb9
6.5840/labrpc.(*Service).dispatch(0xc00043a840, {0x64977f, 0xd}, {{0x5fd460, 0xc00043e580}, {0x64977a, 0x12}, {0x69bb00, 0x5f5e60}, {0xc000ab1e00, ...}, ...})
        /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:494 +0x36e
6.5840/labrpc.(*Server).dispatch(0xc0002f1a88, {{0x5fd460, 0xc00043e580}, {0x64977a, 0x12}, {0x69bb00, 0x5f5e60}, {0xc000ab1e00, 0x168, 0x200}, ...})
        /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:418 +0x1e5
6.5840/labrpc.(*Network).processReq.func1()
        /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:240 +0x3f
created by 6.5840/labrpc.(*Network).processReq in goroutine 46138
        /home/cold-bin/CodeProject/Go Code/6.5840/src/labrpc/labrpc.go:239 +0x1e5
exit status 2
FAIL    6.5840/raft     403.381s
```

索引越界问题，后面在`AppendEntries`限制了提前检测，代码如下：

```go
if rf.lastIncludedIndex > args.PrevLogIndex /*对等点的快照点已经超过本次日志复制的点，没有必要接受此日志复制rpc了*/ {
    reply.Term, reply.Success = rf.currentTerm, false
    return
}
```

### 结果

```bash
➜  raft git:(main) VERBOSE=0 go test -race -run 2D
Test (2D): snapshots basic ...
  Passed -- real time: 4.1       number of Raft peers:3          number of RPC sends: 148        number of bytes:  51216         number of Raft agreements reported: 211
Test (2D): install snapshots (disconnect) ...
  Passed -- real time:40.5       number of Raft peers:3          number of RPC sends:1752        number of bytes: 653103         number of Raft agreements reported: 351
Test (2D): install snapshots (disconnect+unreliable) ...
  Passed -- real time:44.5       number of Raft peers:3          number of RPC sends:1926        number of bytes: 694404         number of Raft agreements reported: 318
Test (2D): install snapshots (crash) ...
  Passed -- real time:29.1       number of Raft peers:3          number of RPC sends:1048        number of bytes: 403904         number of Raft agreements reported: 271
Test (2D): install snapshots (unreliable+crash) ...
  Passed -- real time:32.9       number of Raft peers:3          number of RPC sends:1162        number of bytes: 515722         number of Raft agreements reported: 345
Test (2D): crash and restart all servers ...
  Passed -- real time: 7.4       number of Raft peers:3          number of RPC sends: 240        number of bytes:  69192         number of Raft agreements reported:  49
Test (2D): snapshot initialization after crash ...
  Passed -- real time: 2.5       number of Raft peers:3          number of RPC sends:  78        number of bytes:  21956         number of Raft agreements reported:  14
PASS
ok      6.5840/raft     162.070s
```

**Lab2的运行结果**

目前测试过百次，没有任何报错。

```bash
➜  raft git:(main) time VERBOSE=0 go test -race -run 2 
Test (2A): initial election ...
  Passed -- real time: 3.1       number of Raft peers:3          number of RPC sends:  60        number of bytes:  16534         number of Raft agreements reported:   0
Test (2A): election after network failure ...
  Passed -- real time: 4.4       number of Raft peers:3          number of RPC sends: 120        number of bytes:  24380         number of Raft agreements reported:   0
Test (2A): multiple elections ...
  Passed -- real time: 5.5       number of Raft peers:7          number of RPC sends: 612        number of bytes: 122330         number of Raft agreements reported:   0
Test (2B): basic agreement ...
  Passed -- real time: 1.1       number of Raft peers:3          number of RPC sends:  16        number of bytes:   4450         number of Raft agreements reported:   3
Test (2B): RPC byte count ...
  Passed -- real time: 2.6       number of Raft peers:3          number of RPC sends:  50        number of bytes: 114694         number of Raft agreements reported:  11
Test (2B): test progressive failure of followers ...
  Passed -- real time: 5.1       number of Raft peers:3          number of RPC sends: 122        number of bytes:  26456         number of Raft agreements reported:   3
Test (2B): test failure of leaders ...
  Passed -- real time: 5.3       number of Raft peers:3          number of RPC sends: 194        number of bytes:  42492         number of Raft agreements reported:   3
Test (2B): agreement after follower reconnects ...
  Passed -- real time: 6.1       number of Raft peers:3          number of RPC sends: 124        number of bytes:  33152         number of Raft agreements reported:   8
Test (2B): no agreement if too many followers disconnect ...
  Passed -- real time: 3.7       number of Raft peers:5          number of RPC sends: 196        number of bytes:  42398         number of Raft agreements reported:   3
Test (2B): concurrent Start()s ...
  Passed -- real time: 1.1       number of Raft peers:3          number of RPC sends:  16        number of bytes:   4466         number of Raft agreements reported:   6
Test (2B): rejoin of partitioned leader ...
  Passed -- real time: 6.5       number of Raft peers:3          number of RPC sends: 190        number of bytes:  47097         number of Raft agreements reported:   4
Test (2B): leader backs up quickly over incorrect follower logs ...
  Passed -- real time:25.0       number of Raft peers:5          number of RPC sends:2152        number of bytes:1668628         number of Raft agreements reported: 102
Test (2B): RPC counts aren't too high ...
  Passed -- real time: 2.1       number of Raft peers:3          number of RPC sends:  40        number of bytes:  11626         number of Raft agreements reported:  12
Test (2C): basic persistence ...
  Passed -- real time: 4.4       number of Raft peers:3          number of RPC sends:  90        number of bytes:  23126         number of Raft agreements reported:   6
Test (2C): more persistence ...
  Passed -- real time:19.7       number of Raft peers:5          number of RPC sends:1064        number of bytes: 238546         number of Raft agreements reported:  16
Test (2C): partitioned leader and one follower crash, leader restarts ...
  Passed -- real time: 1.9       number of Raft peers:3          number of RPC sends:  38        number of bytes:   9641         number of Raft agreements reported:   4
Test (2C): Figure 8 ...
  Passed -- real time:27.9       number of Raft peers:5          number of RPC sends: 956        number of bytes: 199519         number of Raft agreements reported:  23
Test (2C): unreliable agreement ...
  Passed -- real time: 5.5       number of Raft peers:5          number of RPC sends: 216        number of bytes:  75706         number of Raft agreements reported: 246
Test (2C): Figure 8 (unreliable) ...
  Passed -- real time:35.3       number of Raft peers:5          number of RPC sends:3140        number of bytes:5542176         number of Raft agreements reported: 609
Test (2C): churn ...
  Passed -- real time:16.6       number of Raft peers:5          number of RPC sends: 780        number of bytes: 328623         number of Raft agreements reported: 376
Test (2C): unreliable churn ...
  Passed -- real time:16.2       number of Raft peers:5          number of RPC sends: 660        number of bytes: 225671         number of Raft agreements reported: 250
Test (2D): snapshots basic ...
  Passed -- real time: 7.0       number of Raft peers:3          number of RPC sends: 138        number of bytes:  49280         number of Raft agreements reported: 234
Test (2D): install snapshots (disconnect) ...
  Passed -- real time:66.4       number of Raft peers:3          number of RPC sends:1468        number of bytes: 531314         number of Raft agreements reported: 299
Test (2D): install snapshots (disconnect+unreliable) ...
  Passed -- real time:85.9       number of Raft peers:3          number of RPC sends:1894        number of bytes: 729100         number of Raft agreements reported: 332
Test (2D): install snapshots (crash) ...
  Passed -- real time:36.7       number of Raft peers:3          number of RPC sends: 720        number of bytes: 344056         number of Raft agreements reported: 342
Test (2D): install snapshots (unreliable+crash) ...
  Passed -- real time:41.3       number of Raft peers:3          number of RPC sends: 804        number of bytes: 429314         number of Raft agreements reported: 355
Test (2D): crash and restart all servers ...
  Passed -- real time:14.6       number of Raft peers:3          number of RPC sends: 288        number of bytes:  83488         number of Raft agreements reported:  62
Test (2D): snapshot initialization after crash ...
  Passed -- real time: 3.9       number of Raft peers:3          number of RPC sends:  72        number of bytes:  20212         number of Raft agreements reported:  14
PASS
ok      6.5840/raft     456.234s
VERBOSE=0 go test -race -run 2  70.15s user 8.20s system 17% cpu 7:36.74 total
```

`race`之后的时间：约78s的CPU时间和总共7m36s的运行时间，满足lab要求的`“当使用`-race`运行时，大约有 10 分钟的实时时间和 2 分钟的 CPU 时间。“`。

***

至此, 终于实现了raft协议🍻