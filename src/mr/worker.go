package mr

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"unsafe"
)
import "log"
import "net/rpc"
import "hash/fnv"

// KeyValue Map functions return a slice of KeyValue.
type KeyValue struct {
	Key   string
	Value string
}

// use ihash(key) % NReduce to choose the reduce
// task number for each KeyValue emitted by Map.
func ihash(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() & 0x7fffffff)
}

// ByKey for sorting by key.
type ByKey []KeyValue

// Len for sorting by key.
func (a ByKey) Len() int           { return len(a) }
func (a ByKey) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByKey) Less(i, j int) bool { return a[i].Key < a[j].Key }

// util functions

// 快速地将string类型转化为[]byte类型
func quickS2B(str string) []byte {
	base := *(*[2]uintptr)(unsafe.Pointer(&str))
	return *(*[]byte)(unsafe.Pointer(&[3]uintptr{base[0], base[1], base[1]}))
}

// 快速地将[]byte转化为string类型
func quickB2S(bs []byte) string {
	base := (*[3]uintptr)(unsafe.Pointer(&bs))
	return *(*string)(unsafe.Pointer(&[2]uintptr{base[0], base[1]}))
}

// reduce任务完成时，需要将临时文件重命名（原子地）
func renameReduceOutFile(tmpf string, taskN int) error {
	return os.Rename(tmpf, fmt.Sprintf("mr-out-%d", taskN))
}

// 拿到map worker里的中间文件
func getIntermediateFIle(nMapTasks, nReduceTasks int) string {
	return fmt.Sprintf("mr-%d-%d", nMapTasks, nReduceTasks)
}

// 把map worker里的中间文件重命名（原子地）
func renameIntermediateFIle(tmpf string, nMapTasks, nReduceTasks int) error {
	return os.Rename(tmpf, getIntermediateFIle(nMapTasks, nReduceTasks))
}

func performMapTask(fn string, taskN int, nReduceTasks int, mapf func(string, string) []KeyValue) {
	f, err := os.Open(fn)
	if err != nil {
		log.Fatalf("open error:%v", err)
	}

	content, err := io.ReadAll(f)
	if err != nil {
		log.Fatalf("read error:%v", err)
	}
	f.Close()

	kvs := mapf(fn, quickB2S(content))

	// 处理中间文件
	tmpFs := make([]*os.File, 0, nReduceTasks)
	tmpFNs := make([]string, 0, nReduceTasks)
	encoders := make([]*json.Encoder, 0, nReduceTasks)

	for r := 0; r < nReduceTasks; r++ {
		tmpF, err := os.CreateTemp("", "")
		if err != nil {
			log.Fatalf("create temporary file error: %v", err)
		}

		tmpFs = append(tmpFs, tmpF)
		tmpFNs = append(tmpFNs, tmpF.Name())
		encoders = append(encoders, json.NewEncoder(tmpF))
	}

	// 写入临时的中间文件
	// 先json编码
	for _, kv := range kvs {
		err := encoders[ihash(kv.Key)%nReduceTasks].Encode(&kv)
		if err != nil {
			log.Fatalf("json encode error: %v", err)
		}
	}

	// 关闭临时文件
	for _, f := range tmpFs {
		f.Close()
	}

	// 原子重命名，其实是为了防止并发读写
	for r := 0; r < nReduceTasks; r++ {
		err := renameIntermediateFIle(tmpFNs[r], taskN, r)
		if err != nil {
			log.Fatalf("rename error: %v", err)
		}
	}
}

func performReduce(taskN, nMapTasks int, reducef func(string, []string) string) {
	kvs := make([]KeyValue, 0)
	for i := 0; i < nMapTasks; i++ {
		intermediatef := getIntermediateFIle(i, taskN)
		f, err := os.Open(intermediatef)
		if err != nil {
			log.Fatalf("open error: %v", err)
		}
		dec := json.NewDecoder(f)
		for {
			var kv KeyValue
			if err := dec.Decode(&kv); err != nil {
				break
			}
			kvs = append(kvs, kv)
		}
		f.Close()
	}

	// 排序
	sort.Sort(ByKey(kvs))

	// 再创建临时文件来写reduce的结果，后面通过原子重命名做到并发读写
	tmpf, err := os.CreateTemp("", "")
	if err != nil {
		log.Fatalf("create temporary file error: %v", err)
	}

	// 把相同的key的所有value聚合在一起
	i := 0
	j := 0
	for i < len(kvs) {
		j = i + 1
		for j < len(kvs) && kvs[j].Key == kvs[i].Key {
			j++
		}
		vs := make([]string, 0, len(kvs))
		for k := i; k < j; k++ {
			vs = append(vs, kvs[k].Value)
		}
		output := reducef(kvs[i].Key, vs)
		_, err := fmt.Fprintf(tmpf, "%v %v\n", kvs[i].Key, output)
		if err != nil {
			log.Fatalf("open or write error: %v", err)
		}
		i = j
	}

	err = renameReduceOutFile(tmpf.Name(), taskN)
	if err != nil {
		log.Fatalf("rename error: %v", err)
	}
}

// Worker main/mrworker.go calls this function.
func Worker(mapf func(string, string) []KeyValue,
	reducef func(string, []string) string) {

	// Your worker implementation here.
	for {
		args := GetTaskArgs{}
		reply := GetTaskReply{}
		call("Coordinator.GetTask", &args, &reply)
		switch reply.TaskType {
		case Map:
			performMapTask(reply.MapFileName, reply.TaskId, reply.NReduceTasks, mapf)
		case Reduce:
			performReduce(reply.TaskId, reply.NMapTasks, reducef)
		case Done:
			return
		default:
			log.Fatal("worker: not support this task type")
		}

		call("Coordinator.FinishedTask",
			&FinishedTaskArgs{TaskId: reply.TaskId, TaskType: reply.TaskType}, &FinishedTaskReply{})
	}
}

// example function to show how to make an RPC call to the coordinator.
//
// the RPC argument and reply types are defined in rpc.go.
func CallExample() {

	// declare an argument structure.
	args := ExampleArgs{}

	// fill in the argument(s).
	args.X = 99

	// declare a reply structure.
	reply := ExampleReply{}

	// send the RPC request, wait for the reply.
	// the "Coordinator.Example" tells the
	// receiving server that we'd like to call
	// the Example() method of struct Coordinator.
	ok := call("Coordinator.Example", &args, &reply)
	if ok {
		// reply.Y should be 100.
		fmt.Printf("reply.Y %v\n", reply.Y)
	} else {
		fmt.Printf("call failed!\n")
	}
}

// send an RPC request to the coordinator, wait for the response.
// usually returns true.
// returns false if something goes wrong.
func call(rpcname string, args interface{}, reply interface{}) bool {
	// c, err := rpc.DialHTTP("tcp", "127.0.0.1"+":1234")
	sockname := coordinatorSock()
	c, err := rpc.DialHTTP("unix", sockname)
	if err != nil {
		log.Fatal("dialing:", err)
	}
	defer c.Close()

	err = c.Call(rpcname, args, reply)
	if err == nil {
		return true
	}

	fmt.Println(err)
	return false
}
