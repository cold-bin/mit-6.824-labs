## lab2C

> 到现在实现的最艰难的一个lab

### 论文总结

### 实现思路

### 调试过程

#### `test 1`调试

我把出版代码写完的时候，所有测试用例都没有过（偶尔会有一次过`test 1`）
后面改了日志打印信息，主要减少了一些不要信息的输出，例如对于日志我们不关心具体是什么，只关心复制到哪了，所以只需要打印相关索引信息即可。
当然，后面也封装了对下标的操作，这样问题也好排查，代码也更好修改。

然后，在日志中看到了：当集群所有节点的日志全部快照了，也就是说快照裁减的日志没有剩下的特殊情况，nextIndex出现了回退现象。

```bash
034063 LOG1 before sendAppendEntries S0, nextIndex:190 matchIndex:189
034064 LOG1 after sendAppendEntries S0, nextIndex:189 matchIndex:189
034071 LOG1 before sendAppendEntries S1, nextIndex:190 matchIndex:189
034072 LOG1 after sendAppendEntries S1, nextIndex:189 matchIndex:189
```
非常令人匪夷所思的bug，然后我在前面的日志中发现，leader发送日志复制rpc，但是follower并没有复制成功，至此问题定位到了：
应该是有个地方的边界情况没有考虑到。下面是关键代码，优化nextIndex定位的部分逻辑。
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

修复的方式有两种：一种是额外处理相等情况；还有一种方法就是，每次快照的时候初始化`rf.log[0].Term`为`lastIncludedTerm`即可。我采用的是第二种。
采用第二种方案就不需要持久化存储`lastIncludedTerm`了

### 结果