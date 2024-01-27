package shardctrler

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

//
// Shard controler: assigns shards to replication groups.
//
// RPC interface:
// Join(servers) -- add a set of groups (gid -> server-list mapping).
// Leave(gids) -- delete a set of groups.
// Move(shard, gid) -- hand off one shard from current owner to gid.
// Query(num) -> fetch Config # num, or latest config if num==-1.
//
// A Config (configuration) describes a set of replica groups, and the
// replica group responsible for each shard. Configs are numbered. Config
// #0 is the initial configuration, with no groups and all shards
// assigned to group 0 (the invalid group).
//
// You will need to add fields to the RPC argument structs.
//

// The number of shards.
const NShards = 10

// A configuration -- an assignment of shards to groups.
// Please don't change this.
type Config struct {
	// config number
	Num int

	// shard -> gid 一个分片对应一个副本组，但是多个分片可以对应一个分组
	Shards [NShards]int

	// gid -> servers[] 一个副本组内使用raft来保证集群内的强一致性
	Groups map[int][]string
}

// deep copy
func (cfg *Config) clone() Config {
	newConfig := Config{
		Num:    cfg.Num,
		Shards: cfg.Shards,
		Groups: make(map[int][]string),
	}

	for gid, servers := range cfg.Groups {
		newConfig.Groups[gid] = append([]string{}, servers...)
	}

	return newConfig
}

const (
	OK = "OK"
)

type Err string

type JoinArgs struct {
	Servers map[int][]string // new GID -> servers mappings

	ClientId   int64
	SequenceId int64
}

type JoinReply struct {
	WrongLeader bool
	Err         Err
}

type LeaveArgs struct {
	GIDs []int

	ClientId   int64
	SequenceId int64
}

type LeaveReply struct {
	WrongLeader bool
	Err         Err
}

type MoveArgs struct {
	Shard int
	GID   int

	ClientId   int64
	SequenceId int64
}

type MoveReply struct {
	WrongLeader bool
	Err         Err
}

type QueryArgs struct {
	Num int // desired config number

	ClientId   int64
	SequenceId int64
}

type QueryReply struct {
	WrongLeader bool
	Err         Err
	Config      Config
}

type SnapshotStatus struct {
	Configs  []Config
	Duptable map[int64]int64
}

type logTopic string

const (
	dClient logTopic = "S_CLNT"
	dError  logTopic = "S_ERRO"
	dInfo   logTopic = "S_INFO"
	dTest   logTopic = "S_TEST"
	dTimer  logTopic = "S_TIMR"
	dWarn   logTopic = "S_WARN"
	dGet    logTopic = "S_GET"
	dPut    logTopic = "S_PUT"
	dAppend logTopic = "S_APPEND"
	dApply  logTopic = "S_APPLY"
	dSnap   logTopic = "S_SNAP"
)

var debugStart time.Time
var debugVerbosity int

func init() {
	debugVerbosity = getVerbosity()
	debugStart = time.Now()

	log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
}

func Debug(topic logTopic, format string, a ...interface{}) {
	if debugVerbosity == 3 || debugVerbosity == -1 {
		time := time.Since(debugStart).Microseconds()
		time /= 100
		prefix := fmt.Sprintf("%06d %v ", time, string(topic))
		format = prefix + format
		log.Printf(format, a...)
	}
}

// Retrieve the verbosity level from an environment variable
func getVerbosity() int {
	v := os.Getenv("VERBOSE")
	level := 0
	if v != "" {
		var err error
		level, err = strconv.Atoi(v)
		if err != nil {
			log.Fatalf("Invalid verbosity %v", v)
		}
	}
	return level
}
