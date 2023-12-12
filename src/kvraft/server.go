package kvraft

import (
	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raft"
	"sync"
	"sync/atomic"
	"time"
)

type OpType string

const (
	GET    OpType = "Get"
	PUT    OpType = "Put"
	APPEND OpType = "Append"
)

// Op 描述Put/Append/Get操作, raft.Logt 中的Command
type Op struct {
	Type       OpType
	Key        string
	Value      string
	ClientID   int64
	SequenceId int64
}

const WaitLeaderTimeout = time.Second // kvserver等待选举的最大时间

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	duptable map[int64]Entry   // 客户端请求的reply去重 key:client id value: rpc reply and sequenceId
	data     map[string]string // 缓存
}

type Entry struct {
	sequenceId int64
	reply      interface{}
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	defer func() {
		Debug(dGet, "S%d(%s) Get args{%+v} reply{%+v}", kv.me, kv.rf.Role(), args, reply)
	}()

	// steps：
	// 1、等待leader选举成功
	if ok := kv.waitLeader(); !ok /*等待超时*/ {
		reply.Value, reply.Err = "", ErrWrongLeader
		return
	}

	// 2、如果不是leader，则return
	if _, leader := kv.rf.GetState(); !leader {
		reply.Value, reply.Err = "", ErrWrongLeader
		return
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()

	// 3、检查重复rpc，如果table里已经有了rpc的reply了，则直接复用return
	if v, ok := kv.duptable[args.ClientId]; ok && v.sequenceId == args.SequenceId {
		oldreply := v.reply.(*GetReply)
		reply.Value, reply.Err = oldreply.Value, oldreply.Err
		return
	}

	// 4、调用Start，先将log放入raft进行log replicate
	op := Op{
		Type:       GET,
		Key:        args.Key,
		ClientID:   args.ClientId,
		SequenceId: args.SequenceId,
	}
	index, _, leader := kv.rf.Start(op)
	if !leader {
		reply.Value, reply.Err = "", ErrWrongLeader
		return
	}

	// 5、kvserver等待底层raft applied后，也就是applyCh管道拿到command，等待执行command
	msg := <-kv.applyCh
	if msg.CommandValid {
		if msg.CommandIndex == index /*是期待的index*/ {
			// 继续后续的步骤
		} else /*不是期待的log*/ {
			Debug(dGet, "not expect log{%+v}: index(%d)!=cmdIndex(%d)", msg, index, msg.CommandIndex)
			reply.Value, reply.Err = "", ErrWrongLeader
			return
		}
	} else if msg.SnapshotValid {
	}

	// 6、再次检查table，是否已经含有reply，如果已经有了，没有必要执行command了；如果没有，则继续执行command，并记录于table中
	if v, ok := kv.duptable[args.ClientId]; ok && v.sequenceId == args.SequenceId {
		oldreply := v.reply.(*GetReply)
		reply.Value, reply.Err = oldreply.Value, oldreply.Err
		return
	} else /*没有重复，则执行command并记录reply*/ {
		val, err := kv.operator(&op)
		reply.Value, reply.Err = val, err
		kv.duptable[args.ClientId] = Entry{
			sequenceId: args.SequenceId,
			reply:      reply,
		}
	}
	// 7、然后reply客户端
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	defer func() {
		Debug(dAppend, "S%d(%s) PutAppend args{%+v} reply{%+v}", kv.me, kv.rf.Role(), args, reply)
	}()
	// steps：
	// 1、等待leader选举成功
	if ok := kv.waitLeader(); !ok /*等待超时*/ {
		reply.Err = ErrWrongLeader
		return
	}
	// 2、如果不是leader，则return
	if _, leader := kv.rf.GetState(); !leader {
		reply.Err = ErrWrongLeader
		return
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()

	// 3、检查重复rpc，如果table里已经有了rpc的reply了，则直接复用return
	if v, ok := kv.duptable[args.ClientId]; ok && v.sequenceId == args.SequenceId {
		oldreply := v.reply.(*PutAppendReply)
		reply.Err = oldreply.Err
		return
	}

	// 4、调用Start，先将log放入raft进行log replicate
	op := Op{
		Type:       OpType(args.Op),
		Key:        args.Key,
		Value:      args.Value,
		ClientID:   args.ClientId,
		SequenceId: args.SequenceId,
	}
	index, _, leader := kv.rf.Start(op)
	if !leader {
		reply.Err = ErrWrongLeader
		return
	}

	// 5、kvserver等待底层raft applied后，也就是applyCh管道拿到command，等待执行command
	msg := <-kv.applyCh
	if msg.CommandValid {
		if msg.CommandIndex == index /*是期待的index*/ {
			// 继续后续的步骤
		} else /*不是期待的log*/ {
			Debug(dAppend, "not expect log{%+v}: index(%d)!=cmdIndex(%d)", msg, index, msg.CommandIndex)
			reply.Err = ErrWrongLeader
			return
		}
	} else if msg.SnapshotValid {
	}

	// 6、再次检查table，是否已经含有reply，如果已经有了，没有必要执行command了；如果没有，则继续执行command，并记录于table中
	if v, ok := kv.duptable[args.ClientId]; ok && v.sequenceId == args.SequenceId {
		oldreply := v.reply.(*PutAppendReply)
		reply.Err = oldreply.Err
		return
	} else /*没有重复，则执行command并记录reply*/ {
		_, reply.Err = kv.operator(&op)
		kv.duptable[args.ClientId] = Entry{
			sequenceId: args.SequenceId,
			reply:      reply,
		}
	}
	// 7、然后reply客户端
}

// Op.Type
//
//	  Get：获取当前key的value，没有则返回“”
//	  Get：获取当前key的value，没有则返回“”
//	  Put：key存在则替换，不存在则新增
//	Append：key存在则追加，不存在则新增
func (kv *KVServer) operator(op *Op) (val string, err Err) {
	defer func() {
		Debug(dInfo, "operator:{%+v},return{val:%s,err:%v}", op, val, err)
	}()
	// operate
	switch op.Type {
	case PUT:
		kv.data[op.Key] = op.Value
		err = OK
	case APPEND:
		kv.data[op.Key] = kv.data[op.Key] + op.Value
		err = OK
	case GET:
		// 看key的存在情况
		if _, ok := kv.data[op.Key]; !ok /*key不存在*/ {
			err = ErrNoKey
		} else /*key存在*/ {
			err = OK
		}
	default:
	}

	val = kv.data[op.Key]
	return
}

// 阻塞到leader出现
func (kv *KVServer) waitLeader() bool {
	timer := time.NewTimer(WaitLeaderTimeout)
	for !kv.killed() {
		select {
		case <-timer.C:
			break
		default:
			if kv.rf.Leader() != raft.NoLeader {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return kv.rf.Leader() != raft.NoLeader
}

// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should data snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})
	applyCh := make(chan raft.ApplyMsg)
	kv := &KVServer{
		me:           me,
		rf:           raft.Make(servers, me, persister, applyCh),
		applyCh:      applyCh,
		dead:         0,
		maxraftstate: maxraftstate,
		duptable:     make(map[int64]Entry),
		data:         make(map[string]string),
	}

	return kv
}
