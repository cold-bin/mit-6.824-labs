package kvraft

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

const (
	OK             Err = "OK"             // 请求成功
	ErrNoKey       Err = "ErrNoKey"       // key 不存在
	ErrWrongLeader Err = "ErrWrongLeader" // 请求的raft实例不是leader
)

type Err string

// Put or Append
type PutAppendArgs struct {
	Key        string
	Value      string
	Op         string // "Put" or "Append"
	ClientId   int64
	SequenceId int64
}

type PutAppendReply struct {
	Err Err
}

type GetArgs struct {
	Key        string
	ClientId   int64
	SequenceId int64
}

type GetReply struct {
	Err   Err
	Value string
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
)

var debugStart time.Time
var debugVerbosity int

func init() {
	debugVerbosity = getVerbosity()
	debugStart = time.Now()

	log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
}

func Debug(topic logTopic, format string, a ...interface{}) {
	if debugVerbosity == 2 || debugVerbosity == -1 {
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
