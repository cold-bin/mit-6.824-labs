## lab4B

lab4B是要求在raft协议库的基础上构建分片键值对服务，需要在支持lab3的Get/Put/Append基础上，新增Config和Shard Add/Remove的支持。实验要求并没有让我们实现如论文里的配置自动切换，这里的配置指代分片与分组的对应关系。

### 实现思路

在前面的lab4A中，我们实现了分片控制器，实现了如何在组间均匀地分配分片，还剩下分片数据如何移动的问题没有解决。lab4B正是解决这个问题。

到这里，raft-extended论文已经没有多少东西可以挖掘的了。实验倒是给了不少tips。

- server应该定期轮询`shardctrler`获取最新配置，并添加代码拒绝不属于自己分组的key请求

- 按照顺序一次处理一个重新配置请求

- 如同lab3的重复检测工作一样仍然需要

- 服务器转移到新配置后，继续存储它不再拥有的分片是可以接受的（尽管这在实际系统中会令人遗憾）。这可能有助于简化您的服务器实现。

- 您可以在 RPC 请求或回复中发送整个地图，这可能有助于保持分片传输代码的简单。

- 引用类型拷贝应使用深拷贝，防止潜在的并发安全问题

- 如果客户端收到 `ErrWrongGroup` ，是否应该更改序列号？如果服务器在执行`Get` / `Put`请求时返回`ErrWrongGroup` ，是否应该更新客户端状态？

  - 当客户端收到`ErrWrongGroup`错误时，这意味着客户端尝试访问的键现在由另一个组管理。在这种情况下，客户端应该重新查询`shardctrler`以获取最新的配置，并根据新的配置重试请求。然而，客户端的序列号（用于跟踪请求以避免重复）不应该在这个过程中更改。这是因为即使请求被重定向到新的组，它仍然是相同的请求，并且应该有相同的序列号。

  - 对于服务器，当它在执行`Get` / `Put`请求时返回`ErrWrongGroup`，它不应该更新其状态。这是因为这个错误表示请求的键不再由这个服务器的组管理，所以这个请求不应该对服务器的状态有任何影响。

- 当组 G1 在配置更改期间需要来自 G2 的分片时，G2 在处理日志条目期间的哪个点将分片发送到 G1 重要吗？

  当然重要啦，而且应该在日志提交并处理后，再发送分片。不能在日志没有提交并处理完毕前发送分片，这样做可能导致不一致性

- 配置更改和分片移动之间存在时间差，配置先行更改时，客户端请求的数据可能还没有在新配置的分组里，这时需要等待，直接拒绝请求，让客户端等待一段时间后重新发起请求即可解决。

### 代码

### `Op`

值得注意的是，分片迁移不只是kv数据，还有一些其他附带的数据都应该携带好。（总不能把duptable丢下把，不然下一次重复检测会在分片迁移过后就失效了）

```go
type Shard struct {
	Data      map[string]string
	ConfigNum int // what version this Shard is in
}

type Op struct {
	ClientId   int64
	SequenceId int
	OpType     OpType

	// for get/put/append
	Key   string
	Value string

	// for config
	UpgradeCfg shardctrler.Config

	// for shard remove/add
	ShardId  int
	Shard    Shard
	Duptable map[int64]int // only for shard add
}
```

### `rpc`

和lab3的实现有点类似，只是需要额外处理分片移动的中间过程：配置更新但是分片还没有移动到位，以及`ErrWrongGroup`的处理（省略`Get`）

```go
func (kv *ShardKV) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	shardId := key2shard(args.Key)

	kv.mu.Lock()
	if kv.cfg.Shards[shardId] != kv.gid {
		reply.Err = ErrWrongGroup
	} else if kv.shards[shardId].Data == nil {
		reply.Err = ShardNotArrived
	}
	kv.mu.Unlock()

	if reply.Err == ErrWrongGroup || reply.Err == ShardNotArrived {
		return
	}

	cmd := Op{
		OpType:     args.Op,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
		Key:        args.Key,
		Value:      args.Value,
	}

	reply.Err = kv.startCmd(cmd, AppOrPutTimeout)

	return
}

func (kv *ShardKV) AddShard(args *SendShardArg, reply *AddShardReply) {
	cmd := Op{
		OpType:     AddShard,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
		ShardId:    args.ShardId,
		Shard:      args.Shard,
		Duptable:   args.Duptable,
	}

	reply.Err = kv.startCmd(cmd, AddShardsTimeout)

	return
}

func (kv *ShardKV) startCmd(cmd Op, timeoutPeriod time.Duration) Err {
	kv.mu.Lock()
	index, _, isLeader := kv.rf.Start(cmd)
	if !isLeader {
		kv.mu.Unlock()
		return ErrWrongLeader
	}

	ch := kv.waitCh(index)
	kv.mu.Unlock()

	select {
	case re := <-ch:
		kv.mu.Lock()
		close(kv.wakeClient[index])
		delete(kv.wakeClient, index)
		if re.SequenceId != cmd.SequenceId || re.ClientId != cmd.ClientId {
			//做到这一点的一种方法是让服务器通过注意到 Start() 返回的索引处出现了不同的请求来检测它是否失去了领导权。
			kv.mu.Unlock()
			return ErrInconsistentData
		}
		kv.mu.Unlock()
		return re.Err
	case <-time.After(timeoutPeriod):
		return ErrOverTime
	}
}
```

### `apply`协程应用日志

```go
func (kv *ShardKV) apply() {
	for !kv.killed() {
		select {
		case msg := <-kv.applyCh:
			if msg.CommandValid {
				kv.mu.Lock()
				op := msg.Command.(Op)
				reply := OpReply{
					ClientId:   op.ClientId,
					SequenceId: op.SequenceId,
					Err:        OK,
				}

				if op.OpType == Put || op.OpType == Get || op.OpType == Append {
					shardId := key2shard(op.Key)

					if kv.cfg.Shards[shardId] != kv.gid /*当前组没有对应key*/ {
						reply.Err = ErrWrongGroup
					} else if kv.shards[shardId].Data == nil /*分片还没有到，需要等待到达*/ {
						reply.Err = ShardNotArrived
					} else /*当前组有对应key*/ {
						if !kv.isDuplicated(op.ClientId, op.SequenceId) {
							kv.duptable[op.ClientId] = op.SequenceId
							switch op.OpType {
							case Put:
								kv.shards[shardId].Data[op.Key] = op.Value
							case Append:
								data := kv.shards[shardId].Data
								builder := strings.Builder{}
								builder.WriteString(data[op.Key])
								builder.WriteString(op.Value)
								data[op.Key] = builder.String()
							case Get:
							default:
								log.Fatalf("invalid cmd type: %v.", op.OpType)
							}
						}
					}
				} else /*config or shard remove/add*/ {
					switch op.OpType {
					case UpgradeConfig:
						kv.upgradeConfig(op)
					case AddShard:
						if kv.cfg.Num < op.SequenceId /*不是最新的配置，等最新配置*/ {
							reply.Err = ConfigNotArrived
						} else /*否则就是最新配置，可以将分片接收*/ {
							kv.addShard(op)
						}
					case RemoveShard:
						kv.removeShard(op)
					default:
						log.Fatalf("invalid cmd type: %v.", op.OpType)
					}
				}

				// 判断快照
				if kv.maxRaftState != -1 && kv.rf.RaftStateSize() > kv.maxRaftState {
					snapshot := kv.encodeSnapShot()
					kv.rf.Snapshot(msg.CommandIndex, snapshot)
				}

				ch := kv.waitCh(msg.CommandIndex)
				kv.mu.Unlock()
				ch <- reply
			} else if msg.SnapshotValid {
				// 快照
				kv.rf.Snapshot(msg.SnapshotIndex, msg.Snapshot)

				// 读取快照的数据
				kv.mu.Lock()
				kv.decodeSnapShot(msg.Snapshot)
				kv.mu.Unlock()
			}
		}
	}
}

// 更新最新的config
func (kv *ShardKV) upgradeConfig(op Op) {
	curConfig := kv.cfg
	upgradeCfg := op.UpgradeCfg

	if curConfig.Num >= upgradeCfg.Num {
		return
	}

	for shard, gid := range upgradeCfg.Shards {
		if gid == kv.gid && curConfig.Shards[shard] == 0 {
			// 如果更新的配置的gid与当前的配置的gid一样且分片为0(未分配）
			kv.shards[shard].Data = make(map[string]string)
			kv.shards[shard].ConfigNum = upgradeCfg.Num
		}
	}

	kv.lastCfg = curConfig
	kv.cfg = upgradeCfg
}

func (kv *ShardKV) addShard(op Op) {
	//此分片已添加或者它是过时的cmd
	if kv.shards[op.ShardId].Data != nil || op.Shard.ConfigNum < kv.cfg.Num {
		return
	}

	kv.shards[op.ShardId] = kv.cloneShard(op.Shard.ConfigNum, op.Shard.Data)

	// 查看发送分片过来的分组，更新seqid
	// 因为当前分片已被当前分组接管，其他附带的数据，诸如duptable也应该拿过来
	for clientId, seqId := range op.Duptable {
		if r, ok := kv.duptable[clientId]; !ok || r < seqId {
			kv.duptable[clientId] = seqId
		}
	}
}

func (kv *ShardKV) removeShard(op Op) {
	if op.SequenceId < kv.cfg.Num {
		return
	}
	kv.shards[op.ShardId].Data = nil
	kv.shards[op.ShardId].ConfigNum = op.SequenceId
	// lab tips: 服务器转移到新配置后，继续存储它不再拥有的分片是可以接受的（尽管这在实际系统中会令人遗憾）。这可能有助于简化您的服务器实现。
	// 这里无需移除duptable
}
```

### `watch`协程轮询配置与分片移动

这部分是lab4B的核心了。大体的流程：

- leader才有分片发送权力，这样可以保证在集群大多数服务器正常工作的情况下正常服务

- 发送前，看看自己有哪些需要发送完

- 然后异步发送后，再GC掉不属于自己分组的分片（raft）

- 如果切片没有都收到，则需要等待一段时间过后再发一次

- 然后，接着处理下一个配置，一个接一个地处理，不能跳跃处理配置

  如此，循环往复直到`killed`

```go
func (kv *ShardKV) watch() {
	kv.mu.Lock()
	curConfig := kv.cfg
	rf := kv.rf
	kv.mu.Unlock()

	for !kv.killed() {
		// only leader needs to deal with configuration tasks
		if _, isLeader := rf.GetState(); !isLeader {
			time.Sleep(UpgradeConfigDuration)
			continue
		}
		kv.mu.Lock()

		// 判断是否把不属于自己的部分给分给别人了
		if !kv.isAllSent() {
			duptable := make(map[int64]int)
			for k, v := range kv.duptable {
				duptable[k] = v
			}
			for shardId, gid := range kv.lastCfg.Shards {
				// 将最新配置里不属于自己的分片分给别人
				if gid == kv.gid && kv.cfg.Shards[shardId] != kv.gid &&
					kv.shards[shardId].ConfigNum < kv.cfg.Num {
					sendDate := kv.cloneShard(kv.cfg.Num, kv.shards[shardId].Data)
					args := SendShardArg{
						Duptable:   duptable,
						ShardId:    shardId,
						Shard:      sendDate,
						ClientId:   int64(gid),
						SequenceId: kv.cfg.Num,
					}

					// shardId -> gid -> server names
					serversList := kv.cfg.Groups[kv.cfg.Shards[shardId]]
					servers := make([]*labrpc.ClientEnd, len(serversList))
					for i, name := range serversList {
						servers[i] = kv.makeEnd(name)
					}

					// 开启协程对每个客户端发送切片(这里发送的应是别的组别，自身的共识组需要raft进行状态修改）
					go func(servers []*labrpc.ClientEnd, args *SendShardArg) {
						index := 0
						start := time.Now()
						for {
							var reply AddShardReply
							// 对自己的共识组内进行add
							ok := servers[index].Call("ShardKV.AddShard", args, &reply)
							// for Challenge1:
							// 如果给予分片成功或者时间超时，这两种情况都需要进行GC掉不属于自己的分片
							if ok && reply.Err == OK || time.Now().Sub(start) >= 2*time.Second {
								// 如果成功
								kv.mu.Lock()
								cmd := Op{
									OpType:     RemoveShard,
									ClientId:   int64(kv.gid),
									SequenceId: kv.cfg.Num,
									ShardId:    args.ShardId,
								}
								kv.mu.Unlock()
								kv.startCmd(cmd, RemoveShardsTimeout)
								break
							}
							index = (index + 1) % len(servers)
							if index == 0 {
								time.Sleep(UpgradeConfigDuration)
							}
						}
					}(servers, &args)
				}
			}
			kv.mu.Unlock()
			time.Sleep(UpgradeConfigDuration)
			continue
		}

		//判断切片是否都收到了
		if !kv.isAllReceived() {
			kv.mu.Unlock()
			time.Sleep(UpgradeConfigDuration)
			continue
		}

		//当前配置已配置，轮询下一个配置
		curConfig = kv.cfg
		mck := kv.mck
		kv.mu.Unlock()

		//按照顺序一次处理一个重新配置请求，不能直接处理最新配置
		newConfig := mck.Query(curConfig.Num + 1)
		if newConfig.Num != curConfig.Num+1 {
			time.Sleep(UpgradeConfigDuration)
			continue
		}

		cmd := Op{
			OpType:     UpgradeConfig,
			ClientId:   int64(kv.gid),
			SequenceId: newConfig.Num,
			UpgradeCfg: newConfig,
		}

		kv.startCmd(cmd, UpConfigTimeout)
	}
}
```



### 结果

```
➜  shardkv git:(main) time VERBOSE=0 go test -race
Test: static shards ...
  ... Passed
Test: join then leave ...
  ... Passed
Test: snapshots, join, and leave ...
  ... Passed
Test: servers miss configuration changes...
  ... Passed
Test: concurrent puts and configuration changes...
  ... Passed
Test: more concurrent puts and configuration changes...
  ... Passed
Test: concurrent configuration change and restart...
  ... Passed
Test: unreliable 1...
  ... Passed
Test: unreliable 2...
  ... Passed
Test: unreliable 3...
  ... Passed
Test: shard deletion (challenge 1) ...
  ... Passed
Test: unaffected shard access (challenge 2) ...
  ... Passed
Test: partial migration shard access (challenge 2) ...
  ... Passed
PASS
ok      6.5840/shardkv  106.350s
VERBOSE=0 go test -race  217.31s user 3.27s system 204% cpu 1:47.61 total
```


