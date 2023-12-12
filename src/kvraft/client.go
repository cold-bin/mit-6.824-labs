package kvraft

import (
	"6.5840/labrpc"
	"time"
)
import "crypto/rand"
import "math/big"

type Clerk struct {
	servers    []*labrpc.ClientEnd
	clientId   int64
	SequenceId int64
	leader     int // 缓存leader，减少轮询
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
		clientId:   nrand(),
		SequenceId: 0,
		leader:     0,
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
		Debug(dGet, "C%d(Seq %d) -> S%d Get {key:%s}", ck.clientId, ck.SequenceId, ck.leader, key)
	}()

	// steps:
	// 1、向leader发送rpc
	// 2、如果网络分区、延时或者已经不再是leader了，那么就找下一个server
	args := &GetArgs{
		Key:        key,
		ClientId:   ck.clientId,
		SequenceId: ck.SequenceId,
	}
	reply := &GetReply{}
	ck.SequenceId++

	server := ck.leader
	for {
		if ok := ck.servers[server].Call("KVServer.Get", args, reply); !ok /*网络分区*/ ||
			reply.Err == ErrWrongLeader /*对等点不是leader*/ {
			server = (server + 1) % len(ck.servers) // 继续发送下一个server
		} else if reply.Err == ErrNoKey || reply.Err == OK {
			// leader成功响应
			ck.leader = server
			if reply.Err == ErrNoKey {
				return ""
			} else if reply.Err == OK {
				return reply.Value
			}
		}
		time.Sleep(10 * time.Millisecond)
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
		Debug(dGet, "C%d(Seq %d) -> S%d %s {key:%s}", ck.clientId, ck.SequenceId, ck.leader, op, key)
	}()
	// like Clerk.Get
	args := &PutAppendArgs{
		Key:        key,
		Value:      value,
		Op:         op,
		ClientId:   ck.clientId,
		SequenceId: ck.SequenceId,
	}
	reply := &PutAppendReply{}
	ck.SequenceId++

	server := ck.leader
	for {
		if ok := ck.servers[server].Call("KVServer.PutAppend", args, reply); !ok /*网络分区*/ ||
			reply.Err == ErrWrongLeader /*对等点不是leader*/ {
			server = (server + 1) % len(ck.servers) // 继续发送下一个server
		} else if reply.Err == ErrNoKey || reply.Err == OK {
			// leader成功响应
			ck.leader = server
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}

func (ck *Clerk) Append(key string, value string) {
	ck.PutAppend(key, value, "Append")
}
