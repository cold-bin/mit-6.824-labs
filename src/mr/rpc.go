package mr

//
// RPC definitions.
//
// remember to capitalize all names.
//

import "os"
import "strconv"

//
// example to show how to declare the arguments
// and reply for an RPC.
//

type ExampleArgs struct {
	X int
}

type ExampleReply struct {
	Y int
}

// Add your RPC definitions here.

// TaskType 任务类型，worker会承担map任务或者reduce任务
type TaskType uint8

const (
	Map TaskType = iota + 1
	Reduce
	Done
)

// GetTaskArgs 没有必要带参数
type GetTaskArgs struct{}

type GetTaskReply struct {
	TaskType     TaskType // 任务类型
	TaskId       int      // 标识任务id（map or reduce）
	NReduceTasks int      // 为map所需，控制中间结果输出到哪一个临时文件
	MapFileName  string   // map的输入文件
	NMapTasks    int      // 为reduce所需，reduce需要从所有map worker中读取中间结果
}

type FinishedTaskArgs struct {
	TaskType TaskType
	TaskId   int // 标识哪一个任务已经完成
}

type FinishedTaskReply struct{}

// Cook up a unique-ish UNIX-domain socket name
// in /var/tmp, for the coordinator.
// Can't use the current directory since
// Athena AFS doesn't support UNIX-domain sockets.
func coordinatorSock() string {
	s := "/var/tmp/5840-mr-"
	s += strconv.Itoa(os.Getuid())
	return s
}
