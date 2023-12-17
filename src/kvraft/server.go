package kvraft

import (
	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raft"
	"bytes"
	"strings"
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
const rpcTimeout = time.Second

// Op 描述Put/Append/Get操作, raft.Logt 中的Command
type Op struct {
	Type       OpType //Put or Append or Get
	Key        string
	Value      string
	ClientId   int64
	SequenceId int64
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big
	persister    *raft.Persister
	lastApplied  int // 用于快照

	duptable   map[int64]int64 //判重
	data       map[string]string
	wakeClient map[int]chan int // 存储每个index处的term，用以唤醒客户端
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	defer func() {
		Debug(dGet, "S%d(%s) Get args{%+v} reply{%+v}", kv.me, kv.rf.Role(), args, reply)
	}()

	op := Op{
		Type:       GET,
		Key:        args.Key,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
	}

	// 如果是leader则会追加成功；否则会失败并返回当前不是leader的错误，客户端定位下一个server
	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Value, reply.Err, reply.Leader = "", ErrNoKey, false
		return
	}

	kv.mu.Lock()
	ch := make(chan int)
	kv.wakeClient[index] = ch
	kv.mu.Unlock()

	// 延迟释放资源
	defer func() {
		go kv.clean(index)
	}()

	select {
	case <-time.After(rpcTimeout) /*超时还没有提交*/ :
	case msgTerm := <-ch /*阻塞等待唤醒*/ :
		if msgTerm == term /*term一样表示当前leader不是过期的leader*/ {
			kv.mu.Lock()
			if val, ok := kv.data[args.Key]; ok {
				reply.Value, reply.Err, reply.Leader = val, OK, true
			} else {
				reply.Value, reply.Err, reply.Leader = "", ErrNoKey, true
			}
			kv.mu.Unlock()
			return
		}
	}

	reply.Value, reply.Err, reply.Leader = "", ErrNoKey, false
	return
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	defer func() {
		Debug(dAppend, "S%d(%s) PutAppend args{%+v} reply{%+v}", kv.me, kv.rf.Role(), args, reply)
	}()

	op := Op{
		Type:       OpType(args.Op),
		Key:        args.Key,
		Value:      args.Value,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
	}

	kv.mu.Lock()
	// 如果follower或leader已经请求过了，可以直接返回了
	if ind, ok := kv.duptable[args.ClientId]; ok && ind >= args.SequenceId {
		kv.mu.Unlock()
		reply.Err, reply.Leader = OK, true
		return
	}
	kv.mu.Unlock()

	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Err, reply.Leader = OK, false
		return
	}

	kv.mu.Lock()
	ch := make(chan int)
	kv.wakeClient[index] = ch
	kv.mu.Unlock()

	defer func() {
		go kv.clean(index)
	}()

	select {
	case <-time.After(rpcTimeout):
	case msgTerm := <-ch /*阻塞等待*/ :
		if msgTerm == term /*term一样表示当前leader不是过期的leader*/ {
			reply.Err, reply.Leader = OK, true
			return
		}
	}

	reply.Err, reply.Leader = OK, false
	return
}

func (kv *KVServer) clean(i int) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	close(kv.wakeClient[i])
	delete(kv.wakeClient, i)
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
// clientId is the index of the current server in servers[].
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
		persister:    persister,
		lastApplied:  0,
		duptable:     make(map[int64]int64),
		data:         make(map[string]string),
		wakeClient:   make(map[int]chan int),
	}

	kv.replay()

	go kv.apply()

	return kv
}

// 快照重放
func (kv *KVServer) replay() {
	snapshot := kv.persister.ReadSnapshot()
	if snapshot == nil || len(snapshot) < 1 {
		return
	}
	snapshotStatus := &SnapshotStatus{}
	if err := labgob.NewDecoder(bytes.NewBuffer(snapshot)).Decode(snapshotStatus); err != nil {
		Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
		return
	}
	kv.lastApplied = snapshotStatus.LastApplied
	kv.data = snapshotStatus.Data
	kv.duptable = snapshotStatus.Duptable
}

func (kv *KVServer) snapshot() {
	if kv.maxraftstate == -1 {
		return
	}

	rate := float64(kv.persister.RaftStateSize()) / float64(kv.maxraftstate)
	if rate >= 0.9 {
		snapshotStatus := &SnapshotStatus{
			LastApplied: kv.lastApplied,
			Data:        kv.data,
			Duptable:    kv.duptable,
		}

		w := new(bytes.Buffer)
		if err := labgob.NewEncoder(w).Encode(snapshotStatus); err != nil {
			Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
			return
		}
		kv.rf.Snapshot(snapshotStatus.LastApplied, w.Bytes())
	}
}

// raft提交的command，应用层应用，这个是针对所有的server
// 对于leader：把所有已提交的command，执行并应用到状态机中；并且leader也需要让客户端reply
// 对于follower：也需要把已提交的command，执行并应用到状态机中；follower没有客户端请求，无需等待
// 应用状态机的时候，只需要应用Put/Append即可，Get不会对状态机造成任何影响
func (kv *KVServer) apply() {
	for msg := range kv.applyCh {

		kv.mu.Lock()
		if msg.CommandValid {
			index := msg.CommandIndex
			term := msg.CommandTerm
			op := msg.Command.(Op)

			if preSequenceId, ok := kv.duptable[op.ClientId]; ok &&
				preSequenceId == op.SequenceId /*应用前需要再判一次重*/ {
			} else /*没有重复，可以应用状态机并记录在table里*/ {
				kv.duptable[op.ClientId] = op.SequenceId
				switch op.Type {
				case PUT:
					kv.data[op.Key] = op.Value
				case APPEND:
					if _, ok := kv.data[op.Key]; ok {
						builder := strings.Builder{}
						builder.WriteString(kv.data[op.Key])
						builder.WriteString(op.Value)
						kv.data[op.Key] = builder.String()
					} else {
						kv.data[op.Key] = op.Value
					}
				case GET:
					/*noting*/
				}
			}

			if ch, ok := kv.wakeClient[index]; ok /*leader唤醒客户端reply*/ {
				Debug(dClient, "S%d wakeup client", kv.me)
				ch <- term
			}
			Debug(dApply, "apply msg{%+v}", msg)
			kv.lastApplied = msg.CommandIndex // 直接依赖底层raft的实现，不在应用层自己维护lastApplied
			kv.snapshot()                     // 将定期快照和follower应用快照串行化处理
		} else if msg.SnapshotValid /*follower使用快照重置状态机*/ {
			snapshotStatus := &SnapshotStatus{}
			if err := labgob.NewDecoder(bytes.NewBuffer(msg.Snapshot)).Decode(snapshotStatus); err != nil {
				Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
				return
			}
			kv.lastApplied = snapshotStatus.LastApplied
			kv.data = snapshotStatus.Data
			kv.duptable = snapshotStatus.Duptable
			Debug(dSnap, "snapshot lastApplied:%d", snapshotStatus.LastApplied)
		}

		kv.mu.Unlock()
	}
}
