## lab4A

### 论文总结

raft-extended论文里对于“如何构建分片存储”提及的较少。lab4A要求实现分片控制器，主要是实现服务端的几个RPC和客户端：

- Join (servers) -- add a set of groups (gid -> server-list mapping).
- Leave (gids) -- delete a set of groups.
- Move (shard, gid) -- hand off one shard from current owner to gid.
- Query (num) -> fetch Config # num, or latest config if num==-1.
- Clerk -- request client

lab4A的实现介绍里给了不少tips：

- 从lab3A的代码框架开始，没有快照要求。这些服务端RPC和客户端的实现和lab3A是一样的，只需要简单改一下即可（例如重复检测可以复用lab3A的代码，去掉快照部分即可）。

- 注意浅拷贝引用类型的情况，避免race bug出现

- 还有一个不太容易发现的tips，Join和Leave RPC都要求尽可能均匀地分配分片和尽可能减少分片移动，也就是需要采取一种负载均衡算法负载副本组的分片（不然某些分组集中了大多数分片的话，分片的意义就不存在了）

  **我刚开始的想法：**

  先排好序{100,50,10,5,5,1}，然后先算平均数（avg）和余数（remainder），每个组都能至少分配avg个分片，排序在前的序列优先分配avg+1，直至分配完remainder。但是还没有想清楚是哪些分片应该分给哪些分组才能尽可能减少移动。我只是解决了尽可能均匀分配分片的问题，但是没有解决尽可能减少分片移动的问题。

  后来参考了这个[博客](https://blog.csdn.net/weixin_45938441/article/details/125386091#t7)

### 实现思路

lab4A的实现总的来说较为简单，可以复用ab3A的代码，最主要的其实是负载均衡的算法实现。

```go

// 此处将 g2ShardCnt 转化为有序slice。目的：出现负载不能均分的情况时，可以根据这个有序列分配哪些序列应该多分配一个分片，
// 从而避免分片移动过多。
//
//	排序的规则：分片数越多排在前面，分片数相同时，gid大的排在前面
func g2ShardCntSortSlice(g2ShardCnt map[int]int) []int {
	gids := make([]int, 0, len(g2ShardCnt))
	for gid, _ := range g2ShardCnt {
		gids = append(gids, gid)
	}
	sort.SliceStable(gids, func(i, j int) bool {
		return g2ShardCnt[gids[i]] > g2ShardCnt[gids[j]] ||
			(g2ShardCnt[gids[i]] == g2ShardCnt[gids[j]] && gids[i] > gids[j])
	})

	return gids
}

// 假设{1,100,50,10,5,5}的序列应该怎么分配才能尽可能减少分片移动？
// 先排好序{100,50,10,5,5,1}，然后先算平均数（avg）和余数（remainder），每个组都能至少分配avg个分片，
// 排序在前的序列优先分配avg+1，直至分配完remainder
//
//	 算法：
//		 0. 分配之前，先确定好当前gid应该被分配多少的分片（avg,avg+1）
//		 1. 先把负载多的释放
//		 2. 再把负载少的分配
func (sc *ShardCtrler) balance(g2ShardCnt map[int]int, oldShards [NShards]int) [NShards]int {
	n := len(g2ShardCnt)
	avg := NShards / n
	remainder := NShards % n
	sortedGids := g2ShardCntSortSlice(g2ShardCnt)

	// 先把负载多的部分释放
	for gidx := 0; gidx < n; gidx++ {
		target := avg
		if gidx >= n-remainder /*前列应该多分配一个*/ {
			target = avg + 1
		}

		// 超出负载
		// 3 3 3 1
		// 3 3 2 1
		if g2ShardCnt[sortedGids[gidx]] > target {
			overLoadGid := sortedGids[gidx]
			changeNum := g2ShardCnt[overLoadGid] - target
			for shard, gid := range oldShards {
				if changeNum <= 0 {
					break
				}
				if gid == overLoadGid {
					oldShards[shard] = 0
					changeNum--
				}
			}
			g2ShardCnt[overLoadGid] = target
		}
	}

	// 为负载少的group分配多出来的group
	for gidx := 0; gidx < n; gidx++ {
		target := avg
		if gidx >= n-remainder {
			target = avg + 1
		}

		if g2ShardCnt[sortedGids[gidx]] < target {
			freeGid := sortedGids[gidx]
			changeNum := target - g2ShardCnt[freeGid]
			for shard, gid := range oldShards {
				if changeNum <= 0 {
					break
				}
				if gid == 0 {
					oldShards[shard] = freeGid
					changeNum--
				}
			}
			g2ShardCnt[freeGid] = target
		}
	}

	return oldShards
}
```

### 结果

脚本测试100次，无fail

```
➜  shardctrler git:(main) time VERBOSE=0 go test -race
Test: Basic leave/join ...
  ... Passed
Test: Historical queries ...
  ... Passed
Test: Move ...
  ... Passed
Test: Concurrent leave/join ...
  ... Passed
Test: Minimal transfers after joins ...
  ... Passed
Test: Minimal transfers after leaves ...
  ... Passed
Test: Multi-group join/leave ...
  ... Passed
Test: Concurrent multi leave/join ...
  ... Passed
Test: Minimal transfers after multijoins ...
  ... Passed
Test: Minimal transfers after multileaves ...
  ... Passed
Test: Check Same config on servers ...
  ... Passed
PASS
ok      6.5840/shardctrler      4.906s
VERBOSE=0 go test -race  3.17s user 0.40s system 65% cpu 5.402 total
```

