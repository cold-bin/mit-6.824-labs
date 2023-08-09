package mr

import (
	"errors"
	"log"
	"sync"
	"time"
)
import "net"
import "os"
import "net/rpc"
import "net/http"

type Coordinator struct {
	// Your definitions here.

	mu   sync.Mutex
	cond *sync.Cond

	mapFiles     []string // map任务的输入文件
	nMapTasks    int      // map任务数，lab里规定的一个map任务对应一个worker。len(mapFiles) = nMapTasks
	nReduceTasks int

	// 记录任务的完成情况和何时分配，用以跟踪worker的处理进度
	mapTasksFinished        []bool      // 记录map任务完成情况
	mapTasksWhenAssigned    []time.Time // 记录map任务何时分配
	reduceTasksFinished     []bool      // 记录reduce任务完成情况
	reduceTasksWhenAssigned []time.Time // 记录reduce任务何时分配

	done bool // 所有reduce任务是否已经完成，也就是整个job是否完成
}

// Your code here -- RPC handlers for the worker to call.

// GetTask worker获取任务
func (c *Coordinator) GetTask(args *GetTaskArgs, reply *GetTaskReply) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply.NMapTasks = c.nMapTasks
	reply.NReduceTasks = c.nReduceTasks

	// 分配worker map任务，直到所有map任务执行完毕
	for {
		mapDone := true
		for i, done := range c.mapTasksFinished {
			if !done /*task没有完成*/ {
				if c.mapTasksWhenAssigned[i].IsZero() || /*任务是否被分配*/
					time.Since(c.mapTasksWhenAssigned[i]).Seconds() > 10 /*分配出去10s还没完成，认为worker寄掉了*/ {
					reply.TaskType = Map
					reply.TaskId = i
					reply.MapFileName = c.mapFiles[i]
					c.mapTasksWhenAssigned[i] = time.Now()
					return nil
				} else {
					mapDone = false
				}
			}
		}

		if !mapDone { /*没有完成，我们需要阻塞*/
			c.cond.Wait()
		} else { /*全部map任务已经完成了*/
			break
		}
	}

	// 此时，所有的map任务已经做完了，可以开始reduce任务了
	for {
		rDone := true
		for i, done := range c.reduceTasksFinished {
			if !done /*task没有完成*/ {
				if c.reduceTasksWhenAssigned[i].IsZero() || /*任务是否被分配*/
					time.Since(c.reduceTasksWhenAssigned[i]).Seconds() > 10 /*分配出去10s还没完成，认为worker寄掉了*/ {
					reply.TaskType = Reduce
					reply.TaskId = i
					c.reduceTasksWhenAssigned[i] = time.Now()
					return nil
				} else {
					rDone = false
				}
			}
		}
		if !rDone { /*没有完成，我们需要等待*/
			c.cond.Wait()
		} else { /*全部map任务已经完成了*/
			break
		}
	}

	// if the job is done
	reply.TaskType = Done
	c.done = true

	return nil
}

func (c *Coordinator) FinishedTask(args *FinishedTaskArgs, reply *FinishedTaskReply) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch args.TaskType {
	case Map:
		c.mapTasksFinished[args.TaskId] = true
	case Reduce:
		c.reduceTasksFinished[args.TaskId] = true
	default:
		return errors.New("coordinator: not support this task type")
	}

	c.cond.Broadcast()

	return nil
}

// Example
//
// an example RPC handler.
// the RPC argument and reply types are defined in rpc.go.
func (c *Coordinator) Example(args *ExampleArgs, reply *ExampleReply) error {
	reply.Y = args.X + 1
	return nil
}

// start a thread that listens for RPCs from worker.go
func (c *Coordinator) server() {
	rpc.Register(c)  // 这里是注册RPC方法到 sync.Map 里
	rpc.HandleHTTP() // 这里是将rpc服务注册到http中
	//l, e := net.Listen("tcp", ":1234")
	sockname := coordinatorSock()
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
}

// Done main/mrcoordinator.go calls Done() periodically to find out
// if the entire job has finished.
func (c *Coordinator) Done() bool {
	ret := false

	// Your code here.
	c.mu.Lock()
	defer c.mu.Unlock()
	ret = c.done
	return ret
}

// MakeCoordinator create a Coordinator.
// main/mrcoordinator.go calls this function.
// nReduce is the number of reduce tasks to use.
func MakeCoordinator(files []string, nReduce int) *Coordinator {
	c := Coordinator{}

	// Your code here.
	c.cond = sync.NewCond(&c.mu)
	c.mapFiles = files
	c.nMapTasks = len(files)
	c.mapTasksFinished = make([]bool, len(files))
	c.mapTasksWhenAssigned = make([]time.Time, len(files))

	c.nReduceTasks = nReduce
	c.reduceTasksWhenAssigned = make([]time.Time, nReduce)
	c.reduceTasksFinished = make([]bool, nReduce)

	// 唤醒
	go func() {
		for {
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
			time.Sleep(time.Second)
		}
	}()

	c.server()
	return &c
}

// 非rpc方法，定时做一些事情：减少并轮询worker存活时间
func (c *Coordinator) timer() {

}
