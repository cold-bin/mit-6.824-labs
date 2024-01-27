package shardctrler

import (
	"6.5840/raft"
	"sort"
	"time"
)
import "6.5840/labrpc"
import "sync"
import "6.5840/labgob"

// ShardCtrler 分片控制器：决定副本组与分片的对应关系（config）
type ShardCtrler struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	persister  *raft.Persister
	duptable   map[int64]int64  //判重
	wakeClient map[int]chan int // 存储每个index处的term，用以唤醒客户端

	configs []Config // indexed by config num
}

type OpType string

const (
	JOIN  OpType = "Join"
	LEAVE OpType = "Leave"
	MOVE  OpType = "Move"
	QUERY OpType = "Query"
)

const rpcTimeout = time.Second

type Op struct {
	Type       OpType
	Args       any
	ClientId   int64
	SequenceId int64
}

// Join (servers) -- add a set of groups (gid -> server-list mapping).
func (sc *ShardCtrler) Join(args *JoinArgs, reply *JoinReply) {
	// 判重
	sc.mu.Lock()
	if preSeq, ok := sc.duptable[args.ClientId]; ok && preSeq >= args.SequenceId {
		reply.WrongLeader, reply.Err = false, OK
		sc.mu.Unlock()
		return
	}
	sc.mu.Unlock()

	op := Op{
		Type:       JOIN,
		Args:       *args,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
	}

	// 先提交给raft
	index, term, isLeader := sc.rf.Start(op)
	if !isLeader {
		reply.WrongLeader, reply.Err = true, OK
		return
	}

	sc.mu.Lock()
	ch := make(chan int)
	sc.wakeClient[index] = ch
	sc.mu.Unlock()

	// 延迟释放资源
	defer func() {
		sc.clean(index)
	}()

	// 等待raft提交
	select {
	case <-time.After(rpcTimeout) /*超时还没有提交*/ :
	case msgTerm := <-ch /*阻塞等待唤醒*/ :
		if msgTerm == term /*term一样表示当前leader不是过期的leader*/ {
			reply.WrongLeader, reply.Err = false, OK
			return
		}
	}

	reply.WrongLeader, reply.Err = true, OK
	return
}

// Leave (gids) -- delete a set of groups.
func (sc *ShardCtrler) Leave(args *LeaveArgs, reply *LeaveReply) {
	// 判重
	sc.mu.Lock()
	if preSeq, ok := sc.duptable[args.ClientId]; ok && preSeq >= args.SequenceId {
		reply.WrongLeader, reply.Err = false, OK
		sc.mu.Unlock()
		return
	}
	sc.mu.Unlock()

	op := Op{
		Type:       LEAVE,
		Args:       *args,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
	}

	// 先提交给raft
	index, term, isLeader := sc.rf.Start(op)
	if !isLeader {
		reply.WrongLeader, reply.Err = true, OK
		return
	}

	sc.mu.Lock()
	ch := make(chan int)
	sc.wakeClient[index] = ch
	sc.mu.Unlock()

	// 延迟释放资源
	defer func() {
		sc.clean(index)
	}()

	// 等待raft提交
	select {
	case <-time.After(rpcTimeout) /*超时还没有提交*/ :
	case msgTerm := <-ch /*阻塞等待唤醒*/ :
		if msgTerm == term /*term一样表示当前leader不是过期的leader*/ {
			reply.WrongLeader, reply.Err = false, OK
			return
		}
	}

	reply.WrongLeader, reply.Err = true, OK
	return
}

// Move (shard, gid) -- hand off one shard from current owner to gid.
func (sc *ShardCtrler) Move(args *MoveArgs, reply *MoveReply) {
	sc.mu.Lock()
	// 判重
	if preSeq, ok := sc.duptable[args.ClientId]; ok && preSeq >= args.SequenceId {
		reply.WrongLeader, reply.Err = false, OK
		sc.mu.Unlock()
		return
	}
	sc.mu.Unlock()

	op := Op{
		Type:       MOVE,
		Args:       *args,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
	}

	// 先提交给raft
	index, term, isLeader := sc.rf.Start(op)
	if !isLeader {
		reply.WrongLeader, reply.Err = true, OK
		return
	}

	sc.mu.Lock()
	ch := make(chan int)
	sc.wakeClient[index] = ch
	sc.mu.Unlock()

	// 延迟释放资源
	defer func() {
		sc.clean(index)
	}()

	// 等待raft提交
	select {
	case <-time.After(rpcTimeout) /*超时还没有提交*/ :
	case msgTerm := <-ch /*阻塞等待唤醒*/ :
		if msgTerm == term /*term一样表示当前leader不是过期的leader*/ {
			reply.WrongLeader, reply.Err = false, OK
			return
		}
	}

	reply.WrongLeader, reply.Err = true, OK
	return
}

// Query (num) -> fetch Config # num, or latest config if num==-1.
func (sc *ShardCtrler) Query(args *QueryArgs, reply *QueryReply) {
	op := Op{
		Type:       QUERY,
		Args:       *args,
		ClientId:   args.ClientId,
		SequenceId: args.SequenceId,
	}

	// 先提交给raft
	index, term, isLeader := sc.rf.Start(op)
	if !isLeader {
		reply.WrongLeader, reply.Err, reply.Config = true, OK, Config{}
		return
	}

	sc.mu.Lock()
	ch := make(chan int)
	sc.wakeClient[index] = ch
	sc.mu.Unlock()

	// 延迟释放资源
	defer func() {
		sc.clean(index)
	}()

	// 等待raft提交
	select {
	case <-time.After(rpcTimeout) /*超时还没有提交*/ :
	case msgTerm := <-ch /*阻塞等待唤醒*/ :
		if msgTerm == term /*term一样表示当前leader不是过期的leader*/ {
			idx := -1
			sc.mu.Lock()
			if args.Num == -1 || args.Num > sc.configs[len(sc.configs)-1].Num /*最新配置*/ {
				idx = len(sc.configs) - 1
			} else /*编号num的配置*/ {
				ok := false
				for i, cfg := range sc.configs {
					if cfg.Num == args.Num {
						idx = i
						ok = true
						break
					}
				}
				if !ok /*配置编号不存在*/ {
					reply.WrongLeader, reply.Err, reply.Config = true, OK, Config{}
					sc.mu.Unlock()
					return
				}
			}
			cfg := sc.configs[idx].clone()
			sc.mu.Unlock()

			reply.WrongLeader, reply.Err, reply.Config = false, OK, cfg
			return
		}
	}

	reply.WrongLeader, reply.Err, reply.Config = true, OK, Config{}
	return
}

func (sc *ShardCtrler) clean(index int) {
	sc.mu.Lock()
	close(sc.wakeClient[index])
	delete(sc.wakeClient, index)
	sc.mu.Unlock()
}

// the tester calls Kill() when a ShardCtrler instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
func (sc *ShardCtrler) Kill() {
	sc.rf.Kill()
	// Your code here, if desired.
}

// needed by shardkv tester
func (sc *ShardCtrler) Raft() *raft.Raft {
	return sc.rf
}

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant shardctrler service.
// me is the index of the current server in servers[].
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister) *ShardCtrler {
	labgob.Register(Op{})
	labgob.Register(JoinArgs{})
	labgob.Register(LeaveArgs{})
	labgob.Register(MoveArgs{})
	labgob.Register(QueryArgs{})

	applyCh := make(chan raft.ApplyMsg)

	sc := &ShardCtrler{
		me:         me,
		rf:         raft.Make(servers, me, persister, applyCh),
		applyCh:    applyCh,
		persister:  persister,
		duptable:   make(map[int64]int64),
		wakeClient: make(map[int]chan int),
	}
	sc.configs = make([]Config, 1)
	sc.configs[0].Groups = map[int][]string{}

	go sc.apply()

	return sc
}

func (sc *ShardCtrler) apply() {
	for msg := range sc.applyCh {
		sc.mu.Lock()
		if msg.CommandValid {
			index := msg.CommandIndex
			term := msg.CommandTerm
			op := msg.Command.(Op)

			if preSequenceId, ok := sc.duptable[op.ClientId]; ok &&
				preSequenceId == op.SequenceId /*应用前需要再判一次重*/ {
			} else /*没有重复，可以应用状态机并记录在table里*/ {
				sc.duptable[op.ClientId] = op.SequenceId
				switch op.Type {
				case JOIN:
					sc.handleJoin(op.Args.(JoinArgs))
				case LEAVE:
					sc.handleLeave(op.Args.(LeaveArgs))
				case MOVE:
					sc.handleMove(op.Args.(MoveArgs))
				case QUERY:
					/*nothing*/
				}
			}

			if ch, ok := sc.wakeClient[index]; ok /*leader唤醒客户端reply*/ {
				Debug(dClient, "S%d wakeup client", sc.me)
				// 避免chan+mutex可能发生的死锁问题
				sc.mu.Unlock()
				ch <- term
				sc.mu.Lock()
			}
		}
		sc.mu.Unlock()
	}
}

func (sc *ShardCtrler) handleJoin(args JoinArgs) {
	oldCfg := sc.configs[len(sc.configs)-1].clone()

	// 新配置中的副本组
	for gid, servers := range args.Servers {
		oldCfg.Groups[gid] = servers
	}

	// 副本组对应的分片数目
	g2ShardCnt := make(map[int]int)
	for gid := range oldCfg.Groups /*初始化*/ {
		g2ShardCnt[gid] = 0
	}
	// 计算分组与分片数
	for _, gid := range oldCfg.Shards {
		if gid != 0 {
			g2ShardCnt[gid]++
		}
	}

	newConfig := Config{
		Num:    len(sc.configs),
		Shards: [10]int{},
		Groups: oldCfg.Groups,
	}
	if len(g2ShardCnt) != 0 {
		newConfig.Shards = sc.balance(g2ShardCnt, oldCfg.Shards)
	}

	sc.configs = append(sc.configs, newConfig)
}

func (sc *ShardCtrler) handleLeave(args LeaveArgs) {
	// 先转化为map方便快速定位gid
	isDelete := make(map[int]bool)
	for _, gid := range args.GIDs {
		isDelete[gid] = true
	}

	oldCfg := sc.configs[len(sc.configs)-1].clone()

	// 删除对应的gid的值
	for _, gid := range args.GIDs {
		delete(oldCfg.Groups, gid)
	}

	g2ShardCnt := make(map[int]int)
	newShard := oldCfg.Shards

	// 初始化 g2ShardCnt
	for gid := range oldCfg.Groups {
		if !isDelete[gid] /*剩下的副本组重新初始化*/ {
			g2ShardCnt[gid] = 0
		}
	}

	for shard, gid := range oldCfg.Shards {
		if gid != 0 {
			if isDelete[gid] /*删除这个副本组*/ {
				newShard[shard] = 0
			} else {
				g2ShardCnt[gid]++
			}
		}
	}

	newConfig := Config{
		Num:    len(sc.configs),
		Shards: [10]int{},
		Groups: oldCfg.Groups,
	}
	if len(g2ShardCnt) != 0 {
		newConfig.Shards = sc.balance(g2ShardCnt, newShard)
	}

	sc.configs = append(sc.configs, newConfig)
}

// 将当前所有者的shard分片移除并交给GID副本组
func (sc *ShardCtrler) handleMove(args MoveArgs) {
	// 创建新的配置
	newConfig := sc.configs[len(sc.configs)-1].clone()
	newConfig.Num++

	// 找到并去掉指定的副本组
	if args.Shard >= NShards && args.Shard < 0 /*不在分片*/ {
		return
	}
	newConfig.Shards[args.Shard] = args.GID

	sc.configs = append(sc.configs, newConfig)
}

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

// 思路来源：https://blog.csdn.net/weixin_45938441/article/details/125386091#t7
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
