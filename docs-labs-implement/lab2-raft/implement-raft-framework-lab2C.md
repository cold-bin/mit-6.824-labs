## lab2C

### 论文总结

见前文[lab2B](implement-raft-framework-lab2B.md)

### 实现思路

lab2C的实现较为简单，会出现问题大多数便是lab2A或者lab2B某些地方实现的可能有问题而导致。这一点在lab2C的指引里也说得很明确。
只需要在raft实例任期、日志或投票发生变化时，才需要持久化。重启的时候，在将以前的状态重新读取出来即可。

当然，lab2C还要求了优化lab2B中nextIndex的定位方法，但是lab2C给了如何做这件事情的方法，实现起来也不难。如下：

**AppendEntries RPC的额外处理**

```go
if len(rf.log) <= args.PrevLogIndex /*可能rf过期，领导者已经应用了很多日志*/ {
    //这种情况下，该raft实例断网一段时间过后，日志落后。所以直接返回 XLen即可。
    //leader更新nextIndex为XLen即可，表示当前raft实例缺少XLen及后面的日志，leader在下次广播时带上这些日志
    // leader   0{0} 1{101 102 103} 5{104}	PrevLogIndex=3	nextIndex=4
    // follower 0{0} 1{101 102 103} 5{104}  PrevLogIndex=3  nextIndex=4
    // follower 0{0} 1{101} 5 			    PrevLogIndex=1  nextIndex=1
    reply.XTerm, reply.XIndex, reply.XLen = -1, -1, len(rf.log)
    reply.Term, reply.Success = rf.currentTerm, false
    return
}

if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm /*冲突：该条目的任期在 prevLogIndex，上不能和 prevLogTerm 匹配上，则返回假*/ {
    // 从后往前找冲突条目，返回最小冲突条目的索引
	// 这种情况较为复杂了，可以参考论文figure 7中的日志情况，对照看便知道为什么是返回最小冲突条目的索引了
    conflictIndex, conflictTerm := -1, rf.log[args.PrevLogIndex].Term
    for i := args.PrevLogIndex; i > rf.commitIndex; i-- {
        if rf.log[i].Term != conflictTerm {
            break
        }
        conflictIndex = i
    }
    reply.XTerm, reply.XIndex, reply.XLen = conflictTerm, conflictIndex, len(rf.log)
    reply.Term, reply.Success = rf.currentTerm, false
    return
}
```

**leader定时发送心跳后请求失败的处理**

```go
// 快速定位nextIndex
if reply.XTerm == -1 && reply.XIndex == -1 { /*Case 3: follower's log is too short*/
    rf.nextIndex[peer] = reply.XLen
    return
}
ok := false
for i, entry := range rf.log { /*Case 2: leader has XTerm*/
    if entry.Term == reply.XTerm {
        ok = true
        rf.nextIndex[peer] = i
    }
}
if !ok { /*Case 1: leader doesn't have XTerm*/
    rf.nextIndex[peer] = reply.XIndex
}
```

### 调试过程

某些地方由于没有做索引检测，导致有些地方在lab2C测试的时候会发生索引越界。下面是心跳请求`AppendEntries`RPC参数的索引校验与矫正
```go
/*解决高并发场景下lab2C里索引越界的问题*/
if args.PrevLogIndex >= 0 && args.PrevLogIndex < len(rf.log) {
    args.PrevLogTerm = rf.log[args.PrevLogIndex].Term
} else {
    args.PrevLogIndex = 0
}
if rf.nextIndex[peer] < 1 {
    rf.nextIndex[peer] = 1
}
```

### 结果

测试总数大于100次，没有出现错误

```bash
➜  raft git:(main) VERBOSE=0 go test -race -run 2C
Test (2C): basic persistence ...
  Passed -- real time: 3.7       number of Raft peers:3          number of RPC sends: 130        number of bytes:  33758         number of Raft agreements reported:   6
Test (2C): more persistence ...
  Passed -- real time:15.6       number of Raft peers:5          number of RPC sends:1504        number of bytes: 345974         number of Raft agreements reported:  16
Test (2C): partitioned leader and one follower crash, leader restarts ...
  Passed -- real time: 1.9       number of Raft peers:3          number of RPC sends:  50        number of bytes:  13015         number of Raft agreements reported:   4
Test (2C): Figure 8 ...
  Passed -- real time:31.3       number of Raft peers:5          number of RPC sends:1404        number of bytes: 304750         number of Raft agreements reported:  32
Test (2C): unreliable agreement ...
  Passed -- real time: 3.4       number of Raft peers:5          number of RPC sends: 240        number of bytes:  82111         number of Raft agreements reported: 246
Test (2C): Figure 8 (unreliable) ...
  Passed -- real time:40.8       number of Raft peers:5          number of RPC sends:6528        number of bytes:10275196        number of Raft agreements reported: 112
Test (2C): churn ...
  Passed -- real time:16.3       number of Raft peers:5          number of RPC sends:1276        number of bytes:1078803         number of Raft agreements reported: 717
Test (2C): unreliable churn ...
  Passed -- real time:16.2       number of Raft peers:5          number of RPC sends:1092        number of bytes:1240526         number of Raft agreements reported: 290
PASS
ok      6.5840/raft     130.406s

```
