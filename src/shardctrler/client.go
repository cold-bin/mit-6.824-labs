package shardctrler

//
// Shardctrler clerk.
//

import "6.5840/labrpc"
import "time"
import "crypto/rand"
import "math/big"

type Clerk struct {
	servers    []*labrpc.ClientEnd
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
		clientId:   nrand(),
		sequenceId: 0,
	}
}

func (ck *Clerk) Query(num int) Config {
	defer func() {
		Debug(dGet, "", ck.clientId, ck.sequenceId)
	}()

	args := &QueryArgs{
		Num:        num,
		ClientId:   ck.clientId,
		SequenceId: ck.sequenceId,
	}
	ck.sequenceId++

	for {
		// try each known server.
		for _, srv := range ck.servers {
			var reply QueryReply
			ok := srv.Call("ShardCtrler.Query", args, &reply)
			if ok && reply.WrongLeader == false {
				return reply.Config
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (ck *Clerk) Join(servers map[int][]string) {
	args := &JoinArgs{
		Servers:    servers,
		ClientId:   ck.clientId,
		SequenceId: ck.sequenceId,
	}
	ck.sequenceId++

	for {
		// try each known server.
		for _, srv := range ck.servers {
			var reply JoinReply
			ok := srv.Call("ShardCtrler.Join", args, &reply)
			if ok && reply.WrongLeader == false {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (ck *Clerk) Leave(gids []int) {
	args := &LeaveArgs{
		GIDs:       gids,
		ClientId:   ck.clientId,
		SequenceId: ck.sequenceId,
	}
	ck.sequenceId++

	for {
		// try each known server.
		for _, srv := range ck.servers {
			var reply LeaveReply
			ok := srv.Call("ShardCtrler.Leave", args, &reply)
			if ok && reply.WrongLeader == false {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (ck *Clerk) Move(shard int, gid int) {
	args := &MoveArgs{
		Shard:      shard,
		GID:        gid,
		ClientId:   ck.clientId,
		SequenceId: ck.sequenceId,
	}
	ck.sequenceId++

	for {
		// try each known server.
		for _, srv := range ck.servers {
			var reply MoveReply
			ok := srv.Call("ShardCtrler.Move", args, &reply)
			if ok && reply.WrongLeader == false {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
