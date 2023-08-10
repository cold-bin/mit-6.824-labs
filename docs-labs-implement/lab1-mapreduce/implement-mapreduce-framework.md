## lab1

### 论文回顾

#### mapreduce架构

严格来讲，MapReduce是一种分布式计算模型，用于解决大于1TB数据量的大数据计算处理。著名的开源项目Hadoop和Spark在计算方面都实现的是MapReduce模型。从论文中可以看到花了不少篇幅在讲解这个模型的原理和运行过程，但同时也花了一点篇幅来讲解处理分布式系统实现中可能遇到的问题。

MapReduce的模型设计很容易进行水平横向扩展以加强系统的能力，基本分为两种任务：map和reduce，通过map任务完成程序逻辑的并发，通过reduce任务完成并发结果的归约和收集，使用这个框架的开发者的任务就是把自己的业务逻辑先分为这两种任务，然后丢给MapReduce模型去运行。设计上，执行这两种任务的worker可以运行在普通的PC机器上，不需要使用太多资源。当系统整体能力不足时，通过增加worker即可解决。

#### 性能瓶颈

那么什么更容易导致系统性能扩展的瓶颈？CPU？内存？磁盘？还是网络？在2004年这篇文章问世的时候回答还是”网络带宽“最受限，论文想方设法的减少数据在系统内的搬运与传输，而到如今数据中心的内网速度要比当时快多了，因此如今更可能的答案恐怕就是磁盘了，新的架构会减少数据持久化到磁盘的次数，更多的利用内存甚至网络（这正是Spark的设计理念）

如何处理较慢的网络？参考论文3.4节减少网络带宽资源的浪费，都尽量让输入数据保存在构成集群机器的本地硬盘上，并通过使用分布式文件系统GFS进行本地磁盘的管理。尝试分配map任务到尽量靠近这个任务的输入数据库的机器上执行，这样从GFS读时大部分还是在本地磁盘读出来。中间数据传输（map到reduce）经过网络一次，但是分多个key并行执行

#### 负载均衡

某个task运行时间比较其他N-1个都长，大家都必须等其结束那就尴尬了，因此参考论文3.5节、3.6节系统设计保证task比worker数量要多，做的快的worker可以继续先执行其他task，减少等待。（框架的任务调度后来发现更值得研究）

#### 容错

参考论文3.3节重新执行那些失败的MR任务即可，因此需要保证MR任务本身是幂等且无状态的。

更特别一些，worker失效如何处理？将失败任务调配到其他worker重新执行，保证最后输出到GFS上的中间结果过程是原子性操作即可。（减少写错数据的可能）

Master失效如何处理？因为master是单点，只能人工干预，系统干脆直接终止，让用户重启重新执行这个计算

#### 其他

其实还有部分工程问题，这篇文章中并没有讨论，可能因为这些更偏重工程实践，比如：task任务的状态如何监控、数据如何移动、worker故障后如何恢复等。

### 实现思路

> lec1中讲到，mapreduce隐藏的细节，我们需要实现下面的这些：
>
> - 将应用程序代码发送到服务器
> - 跟踪哪些任务已完成
> - 将中间数据从 Maps“移到” Reduces
> - 平衡服务器负载
> - 从失败中恢复

#### coordinator

在lab1中，coordinator类似于论文中提到的master，是集群的管理者，负责分配job的task给worker进程。

task分两种：

- 一种是map任务，负责将给定分区文件的数据处理成中间结果，然后将中间结果输出到本地磁盘。输出时，就进行分区。（分区映射函数`hash(key) mod R`）

- 另一种是reduce任务，负责将中间结果收集合并，输出到文件里。

  一般来说，lab1中的一个reduce任务就对应一个输出文件，在map任务输出时，就已经在map worker磁盘本地分好区了。后面reduce任务就会从所有map worker里去取自己分区的中间结果集。

```go
// TaskType 任务类型，worker会承担map任务或者reduce任务
type TaskType uint8

const (
	Map TaskType = iota + 1
	Reduce
	Done
)
```

##### coordinator管理过程

1. 首先将map任务分配给worker，直到所有map任务完成为止
2. 所有map任务完成后，coordinator才开始分发reduce任务

##### coordinator数据结构

> 在上面谈到，lab里并没有要求我们实现一个工业级别的mapreduce，仅仅要求我们实现简易版的demo

- 所有的map任务完成时，才能分配reduce任务

  这也就意味着，如果只是**部分**map任务执行完毕时，我们需要等待其他map任务都执行完毕，才能执行reduce任务。怎么实现呢？在lab的hints里，给出了提示:使用`sync.Cond`条件变量实现

  ```markdown
  当所有的task没有完成时，我们需要调用`sync.Cond`的`Wait`方法等待`Boradcast`唤醒
  
  > 这样做的好处是只需要满足所有map worker都执行完毕才唤醒当前线程，使其执行下一步，也就是分配reduce任务，这条可以避免轮询cpu带来的性能消耗
  ```

- 上面的点提到”当所有task没有完成“这句话，显然，我们还需要记录map任务和reduce任务分别的完成情况。

- 以前google内部的mapreduce（2004）实现在输入前，还会分区。但是lab里只需要将不同文件直接当作不同分区，不再细分为64m的block了，而且一个分区对应一个文件，一个文件对应一个map任务。所以，需要给出分区列表（也就是文件名列表）。

- lab里还要求解决”落伍者“问题。”落伍者“的大概意思就是，如果worker执行太久（lab里规定为10s）而没有finished，那么就认为这个worker寄掉了，此时，我们需要将task重新分配给其他的worker。

- lab中提到，我们需要探测集群中的所有worker是否存活。这里我们是否有必要给worker额外起一个协程来`Ping`/`Pong`吗？

  - 其实我们可以减少这个网络成本。

    worker首先会从coordinator拿task去执行，执行完毕后又会返回task的完成信息。我们认为只要在10s内，worker没有返回了完成信息，那么这个worker就寄掉了。

    > 如果10s过后，完成信息返回到了coordinator，又该怎么办呢？
    >
    > 我们保证确认完成信息是幂等性的就可以了。例如我要在coodinator中更新map任务的完成情况`mapTasksFinished`的核心代码就是
    >
    > ```go
    > mapTasksFinished[task_id] = true
    > ```
    >
    > 我们可以看到，这段代码是幂等的。

    所以，我们需要记录worker的完成时间点和拿取任务时间点的差值是否超过了10s，就代表worker是否寄掉。

- 还有其他数据也需要附带，例如：锁、当前map和reduce任务的数目，job状态

```go
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
```

##### coordinator提供的rpc

由上面得知，我们不需要提供`ping`rpc。我们需要提供申请task和完成task这两个rpc。

**申请task rpc**

task有两种（map和reduce），刚开始我们只能分配map任务，map任务执行完毕过后，才能分配reduce任务。通过`sync.Cond`来实现”等待“。别忘了落伍者的处理。

```go
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
```

**完成task rpc**

当任务完成时，需要回传完成信息。并且，需要唤醒阻塞，进入下一步（可能是等待所有map任务的阻塞，或者是最终等待所有reduce任务的阻塞）

```go
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
```

##### 其他

**定时轮询**

我们其实并不知道何时所有map任务完成或所有reduce任务完成，所以，我们需要轮询去唤醒阻塞，然后检测是否满足条件，不满就继续阻塞等待唤醒

```go
	// 唤醒
	go func() {
		for {
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
			time.Sleep(time.Second)
		}
	}()
```

**原子重命名文件与重命名**

若在`mapf`或之后要使用的`reducef`中，进程由于某些原因退出了，这样产生的文件就是不完整的。但`reduce`任务分不清哪些是成功的`map`任务输出，哪些不是。所以，我们一开始不使用`mr-m-n`形式的文件名，而是创建一个命名不确定的**临时文件**。当所有任务成功完成之后，再把这个文件重命名为正确的`mr-m-n`。

reduce的输出临时文件与map任务差不多。

#### worker

worker的职责就是负责执行map或reduce任务。lab里要求仅仅只是实现一个单机多进程的mapreduce。

1. 首先，worker要去coordinator那里调用拿任务的rpc，刚开始map没执行完毕拿的是map任务；后面执行完毕则拿的是reduce任务；
2. 然后，拿到任务就执行，执行完毕时，需要调用完成任务的rpc，返回coodinator一些信息
3. 最后，不断循环这个过程，以复用worker的资源

```go
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
```

##### 如何执行map任务

1. 首先，打开输入文件，读取所有文件数据（这里lab简单规定一个文件一个分区，对应一个map任务，忽略论文中的分区
2. 然后，调用map函数得到中间结果
3. 再将中间结果序列化写入R个临时文件（先通过哈希函数找到key的分区）（一是可以避免文件名重复，二是为了map和reduce的原子性，例如可以避免中途断电时，使得最后只有一部分结果这种情况。
4. 最后，我们map执行已经完毕了，这时我们再将临时文件重命名为map的中间文件结果，以供reduce任务读取

```go
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

	// 原子重命名，其实是为了防止重名和原子性破坏
	for r := 0; r < nReduceTasks; r++ {
		err := renameIntermediateFIle(tmpFNs[r], taskN, r)
		if err != nil {
			log.Fatalf("rename error: %v", err)
		}
	}
}
```

##### 如何执行reduce任务

1. 首先打开map输出的中间文件，并反序列化文件内容，拿到`(key,list(values))`；
2. 然后对输入列表排序，让中间 key/value pair 数据的处理顺序是按照 key 值增量顺序处理。方便后面实现随机存取；
3. 再次，我们还是通过创建临时文件来保存我们的结果，然后开始聚合相同`key`的数据集；
4. 最后，我们的reduce任务已经完毕了，这时我们再将临时文件重命名为reduce的输出文件。

```go
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

	// 再创建临时文件来写reduce的结果，后面通过原子重命名只有一个最终结果文件
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
```

## 感言

论文其实大部分还是看得懂，最初的时候实现起来毫无头绪，论文给的是一个参考的、极其模糊的实现，而且还是分布式下的。但是lab里没有要求我们需要通过网络来把map worker的中间结果集输送到reduce worker机子里，仅仅只是实现一个单机多进程版本的mapreduce。

这里的实现也是参考了一下lab1 Q&A 才知道我们仅仅只是做一个单机多进程版本的mapreduce，不需要考虑在网络传输map worker的中间结果集。

实现上有很多有意思的地方：

- 中间任务的输出结果，我们写入临时文件里，处理完毕过后，我们再重命名为最终结果文件；如果中途失败，那么我们得不到这个中间文件，因为该文件是临时文件。这样可以保证我们的map任务或reduce任务要么失败没有输出，要么成功有输出，从而保证了原子性
- lab并不像论文中描述的：一旦map任务有一个执行完毕了，那么reduce任务就可以开始启动了。而是采取更简单的实现策略：所有map任务都完成了，才可以启动reduce任务。所以，这里需要有一个等待机制，满足“所有map任务执行完毕”的条件时，我们才能分配reduce任务。所以，这里使用`sync.Cond`来实现。
- 如何实现“落伍者”的检测呢？我们可以记录分配任务时和完成任务时的时间差，太长就认为worker超时了。
