package kvraft

import (
	"6.5840/labrpc"
)
import "crypto/rand"
import "math/big"

type Clerk struct {
	servers    []*labrpc.ClientEnd
	leader     int   // 缓存leader
	clientId   int64 // 标识客户端
	sequenceId int64 // 表示相同客户端发起的不同rpc. 重复rpc具有相同的此项值
}

func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	return &Clerk{
		servers:    servers,
		leader:     0,
		clientId:   nrand(),
		sequenceId: 0,
	}
}

// fetch the current value for a key.
// returns "" if the key does not exist.
// keeps trying forever in the face of all other errors.
//
// you can send an RPC with code like this:
// ok := ck.servers[i].Call("KVServer.Get", &args, &reply)
//
// the types of args and reply (including whether they are pointers)
// must match the declared types of the RPC handler function's
// arguments. and reply must be passed as a pointer.
// 串行调用的，无需上锁（保证每个客户端一次只能一次rpc）
func (ck *Clerk) Get(key string) string {
	defer func() {
		Debug(dGet, "C%d(Seq %d) -> S%d Get {key:%s}", ck.clientId, ck.sequenceId, ck.leader, key)
	}()
	args := &GetArgs{
		Key:        key,
		ClientId:   ck.clientId,
		SequenceId: ck.sequenceId,
	}
	ck.sequenceId++
	server := ck.leader

	for {
		reply := &GetReply{}
		if ok := ck.servers[server].Call("KVServer.Get", args, reply); ok /*网络正常*/ &&
			reply.Leader /*请求的是leader*/ {
			ck.leader = server
			return reply.Value
		} else /*网路分区或请求的不是leader，请求下一个*/ {
			server = (server + 1) % len(ck.servers)
		}
	}
}

// shared by Put and Append.
//
// you can send an RPC with code like this:
// ok := ck.servers[i].Call("KVServer.PutAppend", &args, &reply)
//
// the types of args and reply (including whether they are pointers)
// must match the declared types of the RPC handler function's
// arguments. and reply must be passed as a pointer.
func (ck *Clerk) PutAppend(key string, value string, op string) {
	defer func() {
		Debug(dGet, "C%d(Seq %d) -> S%d %s {key:%s}", ck.clientId, ck.sequenceId, ck.leader, op, key)
	}()
	// like Clerk.Get
	args := &PutAppendArgs{
		Key:        key,
		Value:      value,
		Op:         op,
		ClientId:   ck.clientId,
		SequenceId: ck.sequenceId,
	}
	ck.sequenceId++
	server := ck.leader

	for {
		reply := &PutAppendReply{}
		if ok := ck.servers[server].Call("KVServer.PutAppend", args, reply); ok /*网络正常一定返回 Err=OK*/ &&
			reply.Leader /*请求的是leader*/ {
			ck.leader = server
			return
		} else /*网路分区或请求的不是leader，请求下一个*/ {
			server = (server + 1) % len(ck.servers)
		}
	}
}

func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}

func (ck *Clerk) Append(key string, value string) {
	ck.PutAppend(key, value, "Append")
}
