# paper study notes

## 编程模型

MapReduce仅仅只是一个编程模型，是抽象，并不是具象实现。顾名思义，MapReduce其实就是Map和Reduce。

- Map：输入一个键值对集合，然后输出另外一个键值对集合。

- MapReduce库：将Map函数输出的所有具有相同key值得value值合在一起，然后传递给Reduce函数

- Reduce：不能直接接收Map函数的结果，接受的是MapReduce聚合相同key值后的集合。Reduce函数合并这些value值从而形成一个较小的value值集合。Reduce仅仅只是合并中间键值对。

  > **map函数输入输出的键值对是一个东西吗？**
  >
  > 
  >
  > 答：不是。其实这里的两个键值对并不是同一个语义（编程实现上，应该是叫`type`）。例如，使用MapReduce模型来实现计算一个大的文档集合中每个单词出现的次数的程序
  >
  > ```python
  > map(String key, String value):
  > // key: document name
  > // value: document contents
  > 	for each word w in value:
  > 		EmitIntermediate(w, "1");
  > 
  > reduce(String key, Iterator values):
  > // key: a word
  > // values: a list of counts
  > 	int result = 0;
  > 	for each v in values:
  > 			result += ParseInt(v);
  > 	Emit(AsString(result));
  > ```
  >
  > 在上面的代码示例中，map函数输入的键值对是文档名和文档内容，输出的键值对却是文档里的每个单词以及其出现的次数（这里出现的次数显然是`1`啦）。从这里就剋以看出来，map函数输入与输出的键值对并不具有同一个语义。
  >
  > 
  >
  > **更详细的解释**
  >
  > map(k1,v1) ->list(k2,v2)
  >
  > reduce(k2,list(v2)) ->list(v2)
  >
  > 比如，输入的 key 和 value 值与输出的 key 和 value 值在类型上推导的域不同。此外，中间 key 和 value 值与输出 key 和 value 值在类型上推导的域相同。
  >

## 模型表示

经过前面的描述，我们已经知道了MapReduce其实是一个编程模型，并不是一个具体实现。那么我们应该如何在代码层面上去实现MapReduce呢？这里举几个例子阐述一下。

### case 1：分布式grep

> Linux *grep* (global regular expression) 命令用于查找文件里符合条件的字符串或正则表达式

Map 函数输出匹配某个模式的一行，Reduce 函数是一个恒等函数，即把中间数据复制到输出。这个例子中，Map之后即是最终结果了

### case 2: 计算 URL 访问频率

Map 函数处理日志中 web 页面请求的记录，然后输出(URL,1)。Reduce 函数把相同 URL 的 value 值都累加起来，产生(URL,记录总数)结果。

## 模型实现

模型表示只是给出了大概的实现逻辑，MapReduce还有很多细节问题值得探究。下面是Google内部MapReduce的实现操作流程。

### 执行概况

<img src="https://raw.githubusercontent.com/cold-bin/img-for-cold-bin-blog/master/img/202308031550992.png" alt="image-20230803155035373" style="zoom:67%;" />

图1展示了我们的MapReduce实现中操作的全部流程。当用户调用MapReduce函数时，将发生下面的一系列动作

（下面的序号和图1中的序号一一对应）：

1. 用户程序首先调用的MapReduce库将输入文件分成M个数据片度，每个数据片段的大小一般从16MB到64MB(可以通过可选的参数来控制每个数据片段的大小)。然后用户程序在集群中创建大量的程序副本。
2. 这些程序副本中的有一个特殊的程序--master。副本中其它的程序都是worker程序，由master分配任务。有M 个Map任务和R个Reduce任务将被分配，master将一个Map任务或Reduce任务分配给一个空闲的worker。
3. 被分配了map任务的worker程序读取相关的输入数据片段，从输入的数据片段中解析出key/value pair，然后把key/value pair传递给用户自定义的Map函数，由Map函数生成并输出的中间key/value pair，并缓存在内存中。
4. 缓存中的key/value pair通过分区函数分成R个区域，之后周期性的写入到本地磁盘上。缓存的key/value pair在本地磁盘上的存储位置将被回传给master，由master负责把这些存储位置再传送给Reduce worker。
5. 当Reduce worker程序接收到master程序发来的数据存储位置信息后，使用RPC从Map worker所在主机的磁盘上读取这些缓存数据。当Reduce worker读取了所有的中间数据后，通过对key进行排序后使得具有相同key值的数据聚合在一起。由于许多不同的key值会映射到相同的Reduce任务上，因此必须进行排序。如果中间数据太大无法在内存中完成排序，那么就要在外部进行排序。
6. Reduce worker程序遍历排序后的中间数据，对于每一个唯一的中间key值，Reduce worker程序将这个key值和它相关的中间value值的集合传递给用户自定义的Reduce函数。Reduce函数的输出被追加到所属分区的输出文件。
7. 当所有的Map和Reduce任务都完成之后，master唤醒用户程序。在这个时候，在用户程序里的对MapReduce 调用才返回。

在成功完成任务之后，MapReduce的输出存放在R个输出文件中（对应每个Reduce任务产生一个输出文件，文件名由用户指定）。一般情况下，用户不需要将这R个输出文件合并成一个文件--他们经常把这些文件作为另外一个MapReduce的输入，或者在另外一个可以处理多个分割文件的分布式应用中使用。

### Master数据结构

存储每一个 Map 和 Reduce 任务的状态（空闲、工作中或完成)，以及 Worker 机器(非空闲任务的机器)的标识。Master像是一根数据管道，中间文件存储区域的位置信息通过这根管道从Map传递到Reduce。当 Map 任务完成时，Master接收到位置和大小的更新信息，这些信息被逐步递增的推送给那些正在工作的 Reduce 任务。

### 容错机制

当成千上百的计算机集群时，极低概率发生的故障问题会被放大为大概率事件。当执行MapReduce task时，会被故障机连累，因为所有task执行完毕才算是整个job执行完毕。所以，集群的容错机制是非常重要的。

#### worker故障解决

> 下面worker指的不是worker进程，而是整个worker主机

- **master周期性地`ping`每个worker**

  如果在约定时间内没有收到任何回复，worker就失效了。那么失效worker中 处于完成状态 的map任务 或 正在运行状态的map/reduce任务 都会被重置为初始空闲状态，等待重新调度给有准备的worker来处理。值得注意的是，失效worker完成状态的reduce任务的输出存储在全局文件系统中，无需再次处理了；而已经完成的map任务的输出存储在本机子上，因此由于机子故障也无法访问，所以，我们也需要重新执行已经完成的map任务。

- **正在执行的map任务遇到主机故障后，后面的reduce任务也会变更**

  当一个 Map 任务首先被 worker A 执行，之后由于 worker A 失效了又被调度到 worker B 执行，这个“重新执行”的动作会被通知给所有执行 Reduce 任务的 worker。任何还没有从 worker A 读取数据的 Reduce 任务将从 worker B 读取数据。

- **大规模worker失效的处理**

  在一个 MapReduce 操作执行期间，在正在运行的集群上进行网络维护引起 80 台机器在几分钟内不可访问了，MapReduce master 只需要简单的再次执行那些不可访问的 worker完成的工作，之后继续执行未完成的任务，直到最终完成这个 MapReduce 操作。

#### master故障解决

master的主要定位是整个集群的控制核心，需要分配map任务和reduce任务到确定的worker上。如果worker发生故障时，我们确保master存储的数据结构里的数据不能丢失，所以，我们需要引入持久化存储。也就是，我们需要周期性地将master数据结构里内存的相关数据写入磁盘。这样master故障以后，我们可以从最近的checkpoint处重新执行job剩下的task。

MapReduce的论文中并没有给出一个平滑重启的解决思路。论文中提到的是：仅仅只有一个master进程，如果master寄了，那就利用checkpoint机制，重启master进程即可。

#### 失效方面的处理机制

> 原论文真没看懂(0_o)

### 存储位置

论文中也阐明了网络通信的成本是最高昂的，比读写磁盘还要慢。所以，我们应该尽量减少网络通信带来的高昂成本。其实GFS集群会和MapRduce任务部署在同一个集群上，而且，MapReduce的master在调度map任务时，尽量将一个 Map 任务调度在包含相关输入数据拷贝的机器上执行；如果上述努力失败了，master 将尝试在保存有输入数据拷贝的机器附近的机器上执行 Map 任务（尽可能地减少网络链路长度，类似CDN的原理）

这样做，当在一个足够大的集群上运行MapReduce job时，大部分的数据都是从本地磁盘上读取的，这样可以减少网络通信的成本。

> 现代的MapReduce实际上不再尝试在存储同一台计算机上运行map任务，他们乐于从任何地方获取数据。因为现在的网络很快。

### 任务粒度

> 原论文真没看懂(0_o)

### 备用任务

影响一个 MapReduce 的总执行时间最通常的因素是“落伍者”。为什么这样说？如果某个worker执行的很慢很慢，其他worker都已经执行完task了，那么当前job还在一直等待那个龟速的worker执行完毕。所以说，“落伍者”拖累了整个job。

**那么怎么解决呢？**

当一个 MapReduce 操作接近完成的时候，master 调度备用（backup）任务进程来执行剩下的、处于处理中状态（in-progress）的任务。

其实就是剥夺“落伍者”worker的task，交给其他有准备的worker来处理。

## 扩展功能与优化

虽然前面描述的MapReduce实现已经足够了，但其实还是有一些可以优化的地方。

包括：

- 分区函数：如何将中间key（也就是map的输出）映射到Reduce任务的某一个

- 顺序key：按照key值增量顺序处理。

- Combiner函数：该函数与Reduce函数类似，但是是在本地将map的结果进行合并。

- 跳过损坏记录

  ...

具体实现请转至MapReduce论文。

## refercence

- [MapReduce paper](https://pdos.csail.mit.edu/6.824/papers/mapreduce.pdf)
