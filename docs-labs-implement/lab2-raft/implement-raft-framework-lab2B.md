## lab2B

### 论文回顾与总结

> 原论文比较晦涩，有些地方没有读懂，这里摘抄的B战某up主视频的进一步解释。[解读共识算法Raft（3）日志复制](https://www.bilibili.com/video/BV1VY4y1e7px/?share_source=copy_web&vd_source=107e16d1212e01260cbd925c05ad6eee)

#### 客户端如何定位leader

leader被选举出来后，开始为客户端提供服务，而其他节点接收到客户端请求时需要将请求转向leader。客户端如何如何请求leader呢？

- 第一种情况，请求的节点刚好是leader，则直接成功请求然后进入日志复制和提交状态机的后续工作
- 第二种情况，请求的节点是follower，follower可以通过心跳得知leader节点的id，然后告知客户端
- 第三种情况，请求的节点处于宕机，无法产生响应，那么客户端再去请求其他节点

#### 日志结构

leader接收到客户端的指令后，会把指令作为一个新的条目追加到日志里去。一条日志含有三个信息：

- 状态机指令

- leader任期号

  任期号是raft状态机的逻辑时钟，用于判定节点状态和校验日志是否过期

- 日志索引

  单调递增。如果leader宕机了，那么可能存在日志号相同的情况下，内容不同

需要任期号和日志索引才能唯一确定一条日志

#### 日志复制

- leader并行发送`AppendEntries`RPC给follower，让它们复制该条目，当该条目被超过半数以上的follower复制过后，leader就可以在本地执行该指令并把结果返回给客户端。
- 本地执行指令，也就是leader应用日志到状态机这一步，被称为提交

上面的机制，在leader和follower都能正常运行的情况下，raft能正常工作。但是在分布式场景下，难免会发生一些故障问题，raft需要保证在有宕机的情况下继续支持日志复制，并且保证每个副本日志顺序的一致性。具体有三种可能：

1. 如果有follower因为某些原因没有给leader响应，那么leader会不断地重发追加条目请求（`AppendEntries` RPC），哪怕leader已经没有了响应

2. 如果有follower崩溃后恢复，这是raft追加条目的一致性检查生效，保证follower能按顺序恢复崩溃后缺失的日志

   > raft的一致性检查：leader在每一个发往follower的追加条目RPC中，会放入前一个日志条目的索引位置和任期号，如果follower在它的日志中找不到前一个日志，那么它就会拒绝此日志，leader收到follower的拒接后，会发送前一个日志条目，从而逐渐向前定位到follower第一个缺失的日志，然后按照顺序补齐follower缺失的所有日志
   >
   > 当附加日志 RPC 的请求被拒绝的时候，跟随者可以(返回)冲突条目的任期号和该任期号对应的最小索引地址。

3. 如果leader崩溃，那么崩溃的leader可能已经复制了日志到部分follower但还没有提交而被选出的新leader又可能不具备这些日志，这样就有部分follower中的日志和新leader的日志不相同。

   > raft会在这种情况下，leader通过强制follower复制它的日志来解决不一致的问题，这意味着follower中跟leader冲突的日志条目会被新leader的日志条目覆盖。
   >
   > 当然，由于这些日志没有提交，也就是没有应用到raft状态机里，不违背一致性。

**总结**

- 通过这种机制，leader在当权之后就不需要任何特殊的操作来使日志恢复一致性
- leader只需要进行正常的操作，然后日志就能在回复`AppendEntries`一致性检查失败的时候自动趋于一致
- leader从来不会覆盖或删除自己的日志条目
- 只要过半的节点能正常运行，raft就能接受、复制并应用新的日志条目
- 在正常情况下，新的日志条目可以在一个RPC来回中被复制给集群中过半的机器
- 单个运行慢的follower不会影响整体的性能（超过半数就可以提交日志并返回客户端了）

### 实现思路

最主要实现几个关键的地方：

1. 选举限制

   投票时一定只能投给日志至少和自己一样新的。当集群中超过半数的节点复制了日志，被称为已提交，已提交的日志一定会被应用到状态机里。因为没有获得最新日志的节点无法获得超过半数节点的投票，也就无法成为领导者，所以，领导者的日志只要已经提交，那么就算当前领导者退位，重新选举的领导者一定具备最新的日志。

   ```go
   // 论文里安全性的保证：参数的日志是否至少和自己一样新
   func (rf *Raft) isLogUpToDate(lastLogTerm, lastLogIndex int) bool {
   	return lastLogTerm > rf.lastLogTerm() || (lastLogTerm == rf.lastLogTerm() && lastLogIndex >= rf.lastLogIndex())
   }
   ```

2. 心跳和日志复制应该是同一个实现

   最开始的时候，我是在`Start`方法里添加一致性协议的额外操作：开启额外的协程去做日志复制。但是我后面发现日志复制和心跳的实现逻辑差不多，而且就算含有日志的`AppendEntries`RPC其实也可以算上一次心跳。而且周期地心跳的发送也有助于日志尽快地被复制到其他节点（看了一些网上的实现过后，便将心跳和日志复制整合到一起了。）

   ```go
   
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
   ```

3. AppendEntries` 新日志条目

   ```go
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
   ```

4. 分清楚已提交和已应用区别

   - 已提交：指的是集群中超过半数节点复制了日志
   - 已应用：指的是已提交的日志应用到状态机后的状态

   其中，已提交只能由当前任期的leader去统计集群存在超过半数节点在`N`索引处已经复制了当前任期的日志，这个时候leader的`leaderCommit`才能赋值为`N`.(下面是代码实现)

   **leader是否可以提交**

   ```go
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
       Debug(dLog, `S%d apply to index: %d`, rf.me, N)
       rf.commitIndex = N
       rf.conds[rf.me].Signal()
   }
   ```

   **跟随者提交**

   跟随者是通过领导者心跳或者日志复制RPC时，leaderCommit字段来比较提交的。所以领导者提交日志时，需要等到领导者下一次广播才能让跟随者也跟着提交。

   ```go
   if args.LeaderCommit > rf.commitIndex {
       rf.commitIndex = min(args.LeaderCommit, len(rf.log)-1)
       rf.conds[rf.me].Signal()
   }
   ```

   **应用日志**

   ```go
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
   ```

### 调试过程

调试过程中遇到了一些问题。

- 集群某节点断网一段时间后恢复网络，并且选举出新的leader时，集群中的新日志明明已经复制到了超过半数节点，但是还是无法提交日志。

  通过排查日志发现，重新选举过后，我是按照论文里的把所有节点的nextIndex和matchIndex重新初始化为`领导人最后的日志条目的索引+1`和`0`。但是，由于领导者`AppendEntries`RPC广播时，并不会对自己广播，所以只会去更新跟随者的nextIndex和matchIndex。所以，就导致了集群中节点matchIndex无法在commitIndex上达成大多数，所以就迟迟无法提交。而且，领导者持有最新日志，nextIndex和matchIndex应该初始化为`len(rf.log)`和`len(rf.log) - 1`
  
- `only 2 decided for index 6; wanted 3`表示在索引3处，只有两个server apply，还差一个server没有apply。

  > 在做lab4A前大量测试测出来的bug

  为什么会出现这种情况呢？思考在极端并发下，下面代码会出现什么情况

  ```go
  // 将已提交的日志应用到状态机里。
  // 注意：防止日志被应用状态机之前被裁减掉，也就是说，一定要等日志被应用过后才能被裁减掉。
  func (rf *Raft) applierEvent() {
  	for rf.killed() == false {
  		rf.mu.Lock()
  		rf.cond.Wait() // 等待signal唤醒
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
  ```

  上面的代码在极高并发下，会有这样一个bug：在我们唤醒等待并且释放锁过后，如果此时还有日志提交并调用`signal`那么这个时候会丢掉这次唤醒（唤醒失败），所以上面的写法在极高并发下是有问题的。因为必须在下一次唤醒才能把上一次丢掉的唤醒恢复。

  解决方案：

  - 不再使用applier协程。需要提交的时候直接调用apply及时应用日志，避免唤醒的丢失（不那么优雅
  - 按照条件变量正确用法使用，可以避免丢失唤醒和虚假唤醒问题

  ```go
  // 将已提交的日志应用到状态机里。
  // 注意：防止日志被应用状态机之前被裁减掉，也就是说，一定要等日志被应用过后才能被裁减掉。
  func (rf *Raft) applierEvent() {
  	for rf.killed() == false {
  		rf.mu.Lock()
  		for rf.commitIndex <= rf.lastApplied /*防止虚假唤醒*/ {
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
  ```

### 结果

脚本测试了3000次

```bash
➜  raft git:(main) VERBOSE=0 go test -race -run 2B
Test (2B): basic agreement ...
  Passed -- real time: 1.1       number of Raft peers:3          number of RPC sends:  16        number of bytes:   4028         number of Raft agreements reported:   3
Test (2B): RPC byte count ...
  Passed -- real time: 2.6       number of Raft peers:3          number of RPC sends:  50        number of bytes: 113152         number of Raft agreements reported:  11
Test (2B): test progressive failure of followers ...
  Passed -- real time: 4.9       number of Raft peers:3          number of RPC sends: 124        number of bytes:  25386         number of Raft agreements reported:   3
Test (2B): test failure of leaders ...
  Passed -- real time: 5.4       number of Raft peers:3          number of RPC sends: 191        number of bytes:  40897         number of Raft agreements reported:   3
Test (2B): agreement after follower reconnects ...
  Passed -- real time: 6.2       number of Raft peers:3          number of RPC sends: 129        number of bytes:  31350         number of Raft agreements reported:   8
Test (2B): no agreement if too many followers disconnect ...
  Passed -- real time: 3.9       number of Raft peers:5          number of RPC sends: 218        number of bytes:  43500         number of Raft agreements reported:   3
Test (2B): concurrent Start()s ...
  Passed -- real time: 0.6       number of Raft peers:3          number of RPC sends:   8        number of bytes:   2012         number of Raft agreements reported:   6
Test (2B): rejoin of partitioned leader ...
  Passed -- real time: 6.2       number of Raft peers:3          number of RPC sends: 180        number of bytes:  41682         number of Raft agreements reported:   4
Test (2B): leader backs up quickly over incorrect follower logs ...
  Passed -- real time:33.3       number of Raft peers:5          number of RPC sends:3500        number of bytes:2111589         number of Raft agreements reported: 104
Test (2B): RPC counts aren't too high ...
  Passed -- real time: 2.4       number of Raft peers:3          number of RPC sends:  44        number of bytes:  11512         number of Raft agreements reported:  12
PASS
ok      6.5840/raft     67.725s
```

