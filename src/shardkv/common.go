package shardkv

import "6.5840/shardctrler"

//
// Sharded key/value server.
// Lots of replica groups, each running Raft.
// Shardctrler decides which group serves each shard.
// Shardctrler may change shard assignment from time to time.
//
// You will have to modify these definitions.
//

const (
	OK                  Err = "OK"
	ErrNoKey            Err = "ErrNoKey"
	ErrWrongGroup       Err = "ErrWrongGroup"
	ErrWrongLeader      Err = "ErrWrongLeader"
	ShardNotArrived     Err = "ShardNotArrived"
	ConfigNotArrived    Err = "ConfigNotArrived"
	ErrInconsistentData Err = "ErrInconsistentData"
	ErrOverTime         Err = "ErrOverTime"
)

const (
	Put           OpType = "Put"
	Append        OpType = "Append"
	Get           OpType = "Get"
	UpgradeConfig OpType = "UpgradeConfig"
	AddShard      OpType = "AddShard"
	RemoveShard   OpType = "RemoveShard"
)

type OpType string

type Err string

// Put or Append
type PutAppendArgs struct {
	// You'll have to add definitions here.
	Key        string
	Value      string
	Op         OpType // "Put" or "Append"
	ClientId   int64
	SequenceId int
}

type PutAppendReply struct {
	Err Err
}

type GetArgs struct {
	Key        string
	ClientId   int64
	SequenceId int
}

type GetReply struct {
	Err   Err
	Value string
}

type SendShardArg struct {
	Duptable map[int64]int // for receiver to update its state
	ShardId  int
	Shard    Shard // Shard to be sent
	ClientId int64
	CfgNum   int
}

type AddShardReply struct {
	Err Err
}

type SnapshotStatus struct {
	Shards       []Shard
	Duptable     map[int64]int
	MaxRaftState int
	Cfg          shardctrler.Config
	LastCfg      shardctrler.Config
}
