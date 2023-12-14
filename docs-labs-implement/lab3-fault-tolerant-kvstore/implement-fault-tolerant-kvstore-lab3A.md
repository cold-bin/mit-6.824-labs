## lab3A

### 论文总结

论文里对于客户端的阐述较为简短。提到了以下几点：

- 

### 实现思路

#### LEC 8中Lab3提示的翻译

1. lab3交互草图

   ![image-20231212150951790](assets/image-20231212150951790.png)

2. 如果`Get`或`Put`RPC timeout了(也就是`Call() return false`)，client应该怎么处理？

   > - 如果服务端dead或者请求丢失，client re-send即可
   > - 如果服务器已经执行了请求，但是reply在网络中丢失了，re-send是有危险的

3. 不好区分上面的两种情况。上面两种情况对于client而言，看起来是一样的(no reply)。如果请求已经被执行过了，那么client仍然需要这个reply

   > 让`kvserver`
   检测重复client的请求。client每次请求都会携带一个唯一ID，并在re-send一个rpc时，携带与上次RPC相同的id。`kvserver`
   维护一个按id索引的"duplicate table"，在执行后为每个rpc创建一个条目，用以在"duplicate table"
   中记录reply。如果第二个rpc以相同的id到达，则它是重复的，可以根据"duplicate table"来生成reply。

4. 新的领导者如何获得"duplicate table"？

   > 将 id 放入传递给 Raft 的记录操作中，所有副本都应在执行时更新其table，以便如果它们成为领导者后这些信息依然保留。

5. 如果服务器崩溃，它如何恢复"duplicate table"？

   > - 如果没有快照，日志重播将填充"duplicate table"
   > - 如果有快照，快照必须包含"duplicate table"的副本

6. 如果重复请求在原始请求执行之前到达怎么办？

   再次调用`Start()`，会导致它可能在log中两次。当`cmd`出现在`applyCh`上时，如果table里已经有了，就没有必要执行了。

7. 保持table较小的idea

    - 每个client一个条目，而不是每次 RPC 一个条目

    - 每个client只有一次 RPC 没有完成

      > 每个客户端一次只能有一个未完成的RPC，这意味着客户端在发送一个RPC请求之后，必须等待服务器对该请求的响应，才能发送下一个RPC请求。这样可以确保每个客户端一次只有一个RPC请求在处理中，简化了并发控制。

    - 每个client按顺序对 RPC 进行编号

    - 当服务器收到client 第10个RPC 时，它可以忽略client的较低条目，因为这意味着client永远不会重新发送旧的 RPC

8. 一些细节

    - 每个client都需要一个唯一的client id（64位随机数字即可）
    - client需要在每次rpc中发送client id和seq，如果需要re-send，client就携带相同的seq
    - kvserver中按client id索引的table仅包含 seq 和value（如果已执行）
    - RPC 处理程序首先检查table，只有 seq 大于 table 条目的seq才能执行`Start()`
    - 每个log条目必须包含client id和seq
    - 当operation出现在 `applyCh` 上时，更新client table条目中的 seq 和 value，唤醒正在等待的 RPC 处理程序（如果有）

9. `kvserver`可能会返回table中的旧值，但是返回的确实是当前值，没有问题。

   > C1 C2
   >
   >    \-- --
   > put(x,10)
   > first send of get(x), reply(10) dropped
   > put(x,20)
   > re-sends get(x), server gets 10 from table, not 20
   >
   >   get(x) and put(x,20) run concurrently, so could run before or after;
   > so, returning the remembered value 10 is correct

### 调试过程

**labgob warning: Decoding into a non-default variable/field Err may not work**问题



**`Test: ops complete fast enough (3A) ...`用例**

该用例测试结果显示`Operations completed too slowly 100.000356ms/op > 33.333333ms/op`

表示每次操作花费时间过多，其实这个问题刚开始出现我就猜到是哪出现问题了。主要是我在实现底层raft的时候，应用层调用`Start`后，需要等待到下一次心跳（100ms）才能发送日志同步。所以，应用层调用`Start`过后不能立即同步日志。解决方案就是在`Start`中立即开启一致性协议.

在`Start`中加入下面的代码，用以唤醒log replicate

```go
go func() {
    rf.replicateSignal <- struct{}{}
}()
```

raft实例启动的时候注册并监听下面的事件驱动

```go
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
```



### 结果
