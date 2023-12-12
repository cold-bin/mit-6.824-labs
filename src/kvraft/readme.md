## LEC 8中对于Lab3一些实现提示的翻译

1. lab3交互草图

   ![image-20231212150951790](/home/cold-bin/CodeProject/Go Code/6.5840/src/kvraft/assets/image-20231212150951790.png)

2. 如果`Get`或`Put`RPC timeout了(也就是`Call() return false`)，client应该怎么处理？

   > - 如果服务端dead或者请求丢失，client re-send即可
   > - 如果服务器已经执行了请求，但是reply在网络中丢失了，re-send是有危险的

3. 不好区分上面的两种情况。上面两种情况对于client而言，看起来是一样的(no reply)。如果请求已经被执行过了，那么client仍然需要这个reply

   > 让`kvserver`检测重复client的请求。client每次请求都会携带一个唯一ID，并在re-send一个rpc时，携带与上次RPC相同的id。`kvserver`维护一个按id索引的"duplicate table"，在执行后为每个rpc创建一个条目，用以在"duplicate table"中记录reply。如果第二个rpc以相同的id到达，则它是重复的，可以根据"duplicate table"来生成reply。

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

   >   C1           C2
   >
   >    \--             --
   >   put(x,10)
   >               first send of get(x), reply(10) dropped
   >   put(x,20)
   >               re-sends get(x), server gets 10 from table, not 20
   >
   >   get(x) and put(x,20) run concurrently, so could run before or after;
   >   so, returning the remembered value 10 is correct

   
