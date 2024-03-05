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
	lastApplied  int // 用于快照，由底层raft维护

	duptable   map[int64]int64 //判重
	data       map[string]string
	wakeClient map[int]chan reqIdentification // 存储每个index处的请求编号，用以唤醒对应客户端
}

// 唯一对应一个请求
type reqIdentification struct {
	ClientId   int64
	SequenceId int64
	Err        Err
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
	index, _, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Value, reply.Err, reply.Leader = "", ErrNoKey, false
		return
	}

	kv.mu.Lock()
	ch := make(chan reqIdentification)
	kv.wakeClient[index] = ch
	kv.mu.Unlock()

	// 延迟释放资源
	defer func() {
		kv.clean(index)
	}()

	select {
	case <-time.After(rpcTimeout) /*超时还没有提交*/ :
	case r := <-ch /*阻塞等待唤醒*/ :
		if r.ClientId == args.ClientId && r.SequenceId == args.SequenceId /*是对应请求的响应*/ {
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

	index, _, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Err, reply.Leader = OK, false
		return
	}

	kv.mu.Lock()
	ch := make(chan reqIdentification)
	kv.wakeClient[index] = ch
	kv.mu.Unlock()

	defer func() {
		kv.clean(index)
	}()

	select {
	case <-time.After(rpcTimeout):
	case r := <-ch /*阻塞等待*/ :
		if r.ClientId == args.ClientId && r.SequenceId == args.SequenceId {
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

	// don't close a channel from the receiver side
	// and don't close a channel if the channel has
	// multiple concurrent senders.
	// close(kv.wakeClient[i])

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
		wakeClient:   make(map[int]chan reqIdentification),
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
	kv.data = snapshotStatus.Data
	kv.duptable = snapshotStatus.Duptable
}

func (kv *KVServer) snapshot(lastApplied int) {
	if kv.maxraftstate == -1 {
		return
	}

	rate := float64(kv.persister.RaftStateSize()) / float64(kv.maxraftstate)
	if rate >= 0.9 {
		snapshotStatus := &SnapshotStatus{
			Data:     kv.data,
			Duptable: kv.duptable,
		}

		w := new(bytes.Buffer)
		if err := labgob.NewEncoder(w).Encode(snapshotStatus); err != nil {
			Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
			return
		}
		kv.rf.Snapshot(lastApplied, w.Bytes())
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
			if msg.CommandIndex <= kv.lastApplied /*出现这种情况，快照已经被加载了*/ {
				kv.mu.Unlock()
				continue
			}
			index := msg.CommandIndex
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

			// 主要是为了解决潜在死锁问题
			// 如果客户端还在阻塞，下面这个函数就不会创建容量为1的管道，直接返回
			// 如果客户端没在阻塞，则需要创建容量为1的管道，防止下面发送管道操作阻塞了
			ch := kv.waitCh(index)
			kv.mu.Unlock()
			ch <- reqIdentification{
				ClientId:   op.ClientId,
				SequenceId: op.SequenceId,
				Err:        OK,
			}
			kv.mu.Lock()

			Debug(dApply, "apply msg{%+v}", msg)
			kv.lastApplied = index // 直接依赖底层raft的实现，不在应用层自己维护lastApplied
			kv.snapshot(index)     // 将定期快照和follower应用快照串行化处理
		} else if msg.SnapshotValid /*follower使用快照重置状态机*/ {
			snapshotStatus := &SnapshotStatus{}
			if err := labgob.NewDecoder(bytes.NewBuffer(msg.Snapshot)).Decode(snapshotStatus); err != nil {
				Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
				kv.mu.Unlock()
				return
			}

			kv.lastApplied = msg.SnapshotIndex
			kv.data = snapshotStatus.Data
			kv.duptable = snapshotStatus.Duptable
		}
		kv.mu.Unlock()
	}
}

// 一定会得到chan
func (kv *KVServer) waitCh(index int) chan reqIdentification {
	ch, exist := kv.wakeClient[index]
	if !exist {
		kv.wakeClient[index] = make(chan reqIdentification, 1)
		ch = kv.wakeClient[index]
	}
	return ch
}
