package shardkv

import (
	"bytes"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"sync"

	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raft"
	"6.5840/shardctrler"
)

const (
	UpgradeConfigDuration = 100 * time.Millisecond
	GetTimeout            = 500 * time.Millisecond
	AppOrPutTimeout       = 500 * time.Millisecond
	UpConfigTimeout       = 500 * time.Millisecond
	AddShardsTimeout      = 500 * time.Millisecond
	RemoveShardsTimeout   = 500 * time.Millisecond
)

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

// OpReply is used to wake waiting RPC caller after Op arrived from applyCh
type OpReply struct {
	ClientId   int64
	SequenceId int
	Err        Err
}

type ShardKV struct {
	mu           sync.Mutex
	me           int
	rf           *raft.Raft
	applyCh      chan raft.ApplyMsg
	makeEnd      func(string) *labrpc.ClientEnd
	gid          int
	masters      []*labrpc.ClientEnd
	maxRaftState int                // snapshot if log grows this big
	dead         int32              // set by Kill()
	cfg          shardctrler.Config // 需要更新的最新的配置
	lastCfg      shardctrler.Config // 更新之前的配置，用于比对是否全部更新完了
	shards       []Shard            // ShardId -> Shard 如果Data == nil则说明当前的数据不归当前分片管
	wakeClient   map[int]chan OpReply
	duptable     map[int64]int
	mck          *shardctrler.Clerk
}

func (kv *ShardKV) Get(args *GetArgs, reply *GetReply) {
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
		OpType:     Get,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
		Key:        args.Key,
	}

	if err := kv.startCmd(cmd, GetTimeout); err != OK {
		reply.Err = err
		return
	}

	kv.mu.Lock()
	if kv.cfg.Shards[shardId] != kv.gid {
		reply.Err = ErrWrongGroup
	} else if kv.shards[shardId].Data == nil {
		reply.Err = ShardNotArrived
	} else {
		reply.Err = OK
		reply.Value = kv.shards[shardId].Data[args.Key]
	}
	kv.mu.Unlock()

	return
}

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

// AddShard move shards from caller to this server
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

// servers[] contains the ports of the servers in this group.
//
// me is the index of the current server in servers[].
//
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
//
// the k/v server should snapshot when Raft's saved state exceeds
// maxRaftState bytes, in order to allow Raft to garbage-collect its
// log. if maxRaftState is -1, you don't need to snapshot.
//
// gid is this group's GID, for interacting with the shardctrler.
//
// pass masters[] to shardctrler.MakeClerk() so you can send
// RPCs to the shardctrler.
//
// make_end(servername) turns a server name from a
// UpgradeCfg.Groups[gid][i] into a labrpc.ClientEnd on which you can
// send RPCs. You'll need this to send RPCs to other groups.
//
// look at client.go for examples of how to use masters[]
// and make_end() to send RPCs to the group owning a specific Shard.
//
// StartServer() must return quickly, so it should start goroutines
// for any long-running work.
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int,
	gid int, masters []*labrpc.ClientEnd, make_end func(string) *labrpc.ClientEnd) *ShardKV {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.

	// Your initialization code here.
	// Use something like this to talk to the shardctrler:
	// kv.mck = shardctrler.MakeClerk(kv.ctrlers)
	labgob.Register(Op{})

	applych := make(chan raft.ApplyMsg)
	kv := &ShardKV{
		me:           me,
		rf:           raft.Make(servers, me, persister, applych),
		applyCh:      applych,
		makeEnd:      make_end,
		gid:          gid,
		masters:      masters,
		maxRaftState: maxraftstate,
		dead:         0,
		shards:       make([]Shard, shardctrler.NShards),
		wakeClient:   make(map[int]chan OpReply),
		duptable:     make(map[int64]int),
		mck:          shardctrler.MakeClerk(masters),
	}

	if snapshot := persister.ReadSnapshot(); len(snapshot) > 0 {
		kv.decodeSnapShot(snapshot)
	}

	go kv.apply()
	go kv.watch()

	return kv
}

// apply 处理applyCh发送过来的ApplyMsg
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

// 配置监控
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

func (kv *ShardKV) encodeSnapShot() []byte {
	w := new(bytes.Buffer)
	status := SnapshotStatus{
		Shards:       kv.shards,
		Duptable:     kv.duptable,
		MaxRaftState: kv.maxRaftState,
		Cfg:          kv.cfg,
		LastCfg:      kv.lastCfg,
	}
	if err := labgob.NewEncoder(w).Encode(status); err != nil {
		log.Fatalf("[%d-%d] fails to take snapshot.", kv.gid, kv.me)
	}
	return w.Bytes()
}

func (kv *ShardKV) decodeSnapShot(snapshot []byte) {
	if snapshot == nil || len(snapshot) < 1 { // bootstrap without any state?
		return
	}
	r := bytes.NewBuffer(snapshot)
	var status SnapshotStatus
	if labgob.NewDecoder(r).Decode(&status) != nil {
		log.Fatalf("[Server(%v)] Failed to decode snapshot！！！", kv.me)
	} else {
		kv.shards = status.Shards
		kv.duptable = status.Duptable
		kv.maxRaftState = status.MaxRaftState
		kv.cfg = status.Cfg
		kv.lastCfg = status.LastCfg
	}
}

// Kill the tester calls Kill() when a ShardKV instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
func (kv *ShardKV) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *ShardKV) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

// 重复检测
func (kv *ShardKV) isDuplicated(clientId int64, seqId int) bool {
	lastSeqId, exist := kv.duptable[clientId]
	if !exist {
		return false
	}
	return seqId <= lastSeqId
}

func (kv *ShardKV) waitCh(index int) chan OpReply {
	ch, exist := kv.wakeClient[index]
	if !exist {
		kv.wakeClient[index] = make(chan OpReply, 1)
		ch = kv.wakeClient[index]
	}
	return ch
}

func (kv *ShardKV) isAllSent() bool {
	for shard, gid := range kv.lastCfg.Shards {
		// 如果当前配置中分片的信息不匹配，且持久化中的配置号更小，说明还未发送
		if gid == kv.gid && kv.cfg.Shards[shard] != kv.gid && kv.shards[shard].ConfigNum < kv.cfg.Num {
			return false
		}
	}
	return true
}

func (kv *ShardKV) isAllReceived() bool {
	for shard, gid := range kv.lastCfg.Shards {
		// 判断切片是否都收到了
		if gid != kv.gid && kv.cfg.Shards[shard] == kv.gid && kv.shards[shard].ConfigNum < kv.cfg.Num {
			return false
		}
	}
	return true
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

func (kv *ShardKV) cloneShard(ConfigNum int, data map[string]string) Shard {
	migrateShard := Shard{
		Data:      make(map[string]string),
		ConfigNum: ConfigNum,
	}

	for k, v := range data {
		migrateShard.Data[k] = v
	}

	return migrateShard
}
