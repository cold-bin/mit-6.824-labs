## lab3B

> 实现的最轻松的lab

### 论文总结

论文对于lab3B的实现提及较少，主要是以下几点

- 重新启动kvserver时，如果存在快照，则直接把快照里的数据放到状态机里
- kvserver需要检测`RaftStateSize`是否接近`maxraftstate`，如果大于就快照
- 应用层从`applyCh`管道中接收到的follower快照需要替换当前状态机，以进行快速恢复

### 实现思路

#### 快照

 每个server都需要监控

```go
func (kv *KVServer) snapshot() {
	if kv.maxraftstate == -1 {
		return
	}

	rate := float64(kv.persister.RaftStateSize()) / float64(kv.maxraftstate)
	if rate >= 0.9 {
		snapshotStatus := &SnapshotStatus{
			LastApplied: kv.lastApplied,
			Data:        kv.data,
			Duptable:    kv.duptable,
		}

        w := new(bytes.Buffer)
        if err := labgob.NewEncoder(w).Encode(snapshotStatus); err != nil {
            Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
            return
        }

        kv.rf.Snapshot(snapshotStatus.LastApplied, w.Bytes())
	}
}
```

#### `apply`中快照处理

```go
func (kv *KVServer) apply() {
	for msg := range kv.applyCh {
		kv.mu.Lock()
		if msg.CommandValid {
            // todo 应用已提交的log
            kv.snapshot()
		} else if msg.SnapshotValid /*follower使用快照重置状态机*/ {
			snapshotStatus := &SnapshotStatus{}
			if err := labgob.NewDecoder(bytes.NewBuffer(msg.Snapshot)).Decode(snapshotStatus); err != nil {
				Debug(dError, "snapshot gob encode snapshotStatus err:%v", err)
				return
			}
			kv.lastApplied = snapshotStatus.LastApplied
			kv.data = snapshotStatus.Data
			kv.duptable = snapshotStatus.Duptable
			Debug(dSnap, "snapshot lastApplied:%d", snapshotStatus.LastApplied)
		}
		kv.mu.Unlock()
	}
}
```

### 调试过程

花了差不多一个小时便实现了初版，但是测试偶尔能pass，偶尔只fail掉下面三个测试用例。

**Test: restarts, snapshots, many clients (3B) ...**用例

该用例显示“duplicate element x 17 11 y in Append result”，表示重复log。

主要是还没有实现lab3B tips中的`Your kvserver must be able to detect duplicated operations in the log across checkpoints, so any state you are using to detect them must be included in the snapshots.`

我的实现是快照的时候，将kv`data`和`duplicate table`一起快照，重放的时候`duplicate table`代替即可。

**Test: restarts, snapshots, many clients (3B) ...**用例

较少出现的FAIL：

```
get wrong value, key 8, 
        wanted:
        x 8 0 yx 8 1 yx 8 2 yx 8 3 yx 8 4 yx 8 5 yx 8 6 yx 8 7 yx 8 8 yx 8 9 yx 8 10 yx 8 11 yx 8 12 yx 8 13 yx 8 14 yx 8 15 yx 8 16 y
        got:
        x 8 0 yx 8 1 yx 8 2 yx 8 3 yx 8 4 yx 8 5 yx 8 6 yx 8 7 yx 8 8 yx 8 9 yx 8 10 yx 8 11 yx 8 12 yx 8 13 yx 8 14 yx 8 15 y
```

~~初步猜测是自己在实现lab 2D残留的bug导致的，后面找时间解决解决一下。~~

不清楚问题出现在了哪里，参考了[博客](https://blog.csdn.net/qq_41703198/article/details/127272977)的说法

> 几个可能出现的错误与原因: 
>
> - get命令发现某一个value值缺了中间一个，如1，2，3，5，6
> - 值最后缺了一个，如1，2，3，4 但应该是1，2，3，4，5
>
> 这两种情况很可能是接收了snapshot后修改了lastapplied，但是常规apply循环此时并没有结束，因而自增了lastapplied
>
> 所以，我这里就把apply和snapshot串行化了，不再并发协程处理了。

主要是把以前的定期快照和follower应用快照进行串行化处理，然后快照的时候，一定要先copy锁释放前的状态，然后新开协程执行锁前的快照。（qwq，后面运行了几百次算是解决了这个问题

但是这又会引起新的问题：因为只有从`applyCh`管道收到已提交的日志，才会让kvserver快照。如果在管道收到所有已提交的日志前，客户端请求速度较快较多，会导致日志不断堆积，从而使日志可能会超过测试用例给的限制。这种情况下目前还没有解决。

**Test: unreliable net, snapshots, many clients (3B) ...**用例

偶尔出现`test took longer than 120 seconds`问题，主要是我在机子上并发运行很多个测试，导致cpu满负载运行，有时候测试用例会饥饿导致超时。

**某些test可能会出现死锁问题**

出现死锁的问题，大概是某些地方解锁或开锁不正确，或者chan一直阻塞没有发送或者接收，还有可能是channel+互斥锁错误混用的问题。后面把一些原来混用的情况，改了一下。

```go
// 避免 chan+mutex 可能发生的死锁问题
kv.mu.Unlock()
ch <- term
kv.mu.Lock()
```

但是这个解决方案又会导致下面的问题出现。

**偶尔会出现`panic: send on closed channel`**

客户端超时会直接返回并释放资源，这个时候可能由于下面代码锁释放过后，就被资源释放操作拿到锁了。也就是说，可能出现释放chan资源在向chan发送数据之前，所以就可能导致这个问题。

解决的方案有两种：

- 更强的线性化：保证释放chan资源一定在向chan发送数据之前，这个可能需要添加额外的保证才行了（例如channel）。
- 直接捕获，按照正常情况处理（我采用的这个，但是这个方案使用`-race`的话会测试失败，及时`recover`也会被`race`检测出来

```go
if ch, ok := kv.wakeClient[index]; ok /*leader唤醒客户端reply*/ {
    Debug(dClient, "S%d wakeup client", kv.me)
    // 避免chan+mutex可能发生的死锁问题
    kv.mu.Unlock()
    func() /*退栈，确保recover捕获nil chan*/ {
        defer func() {
            if r := recover(); r != nil /*大概是客户端超时释放资源了*/ {
                Debug(dInfo, "S%d close nil chan", kv.me)
                return
            }
        }()
        ch <- term
    }()
    kv.mu.Lock()
}
```

### 结果

```bash
➜  kvraft git:(main) time go test -race -run 3B
Test: InstallSnapshot RPC (3B) ...
  ... Passed --   4.2  3   725   63
Test: snapshot size is reasonable (3B) ...
  ... Passed --   5.6  3  3036  800
Test: ops complete fast enough (3B) ...
  ... Passed --   8.2  3  3422    0
Test: restarts, snapshots, one client (3B) ...
  ... Passed --  19.2  5  7162 1066
Test: restarts, snapshots, many clients (3B) ...
  ... Passed --  22.3  5 11811 1545
Test: unreliable net, snapshots, many clients (3B) ...
  ... Passed --  16.2  5  7496 1070
Test: unreliable net, restarts, snapshots, many clients (3B) ...
  ... Passed --  20.3  5  9681 1247
Test: unreliable net, restarts, partitions, snapshots, many clients (3B) ...
  ... Passed --  27.9  5  7494  782
Test: unreliable net, restarts, partitions, snapshots, random keys, many clients (3B) ...
  ... Passed --  29.1  7 18583 1424
PASS
ok      6.5840/kvraft   154.159s
go test -race -run 3B  140.72s user 4.55s system 93% cpu 2:34.80 total
```

