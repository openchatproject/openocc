package consensus

import (
	"encoding/hex"
	"github.com/OpenOCC/OCC/occclient"
	"github.com/OpenOCC/OCC/encapdb"
	"sync"
	"time"

	"github.com/OpenOCC/OCC/blockchain"
	"github.com/OpenOCC/OCC/conf"
	"github.com/OpenOCC/OCC/core/types"
	"github.com/OpenOCC/OCC/ctxlog"
	"github.com/OpenOCC/OCC/db"
	"github.com/OpenOCC/OCC/log"
	"github.com/OpenOCC/OCC/param"
)

type DbftConsensus struct {
	Round        *types.Round
	Blockchain   *blockchain.BlockChain
	BlockManager *blockchain.BlockManager
	VoteResults  blockchain.VoteResults
	Client       occclient.IClient
	Locker       sync.RWMutex
}

func NewDbftConsensus(Blockchain *blockchain.BlockChain, client occclient.IClient) *DbftConsensus {
	return &DbftConsensus{
		Round:        types.NewRound(param.MainChainDelegateNode, -1, 0),
		Blockchain:   Blockchain,
		BlockManager: blockchain.NewBlockManager(),
		VoteResults:  blockchain.NewVoteResults(),
		Client:       client,
		Locker:       sync.RWMutex{},
	}
}

func (dbft DbftConsensus) GetRound() types.Round {
	return dbft.Round.Clone()
}

// 校验从其他委托人节点过来的区块数据
func (dbft DbftConsensus) BlockFromPeer(ctxlog *ctxlog.ContextLog, block blockchain.Block) {
	dbft.Locker.Lock()
	defer dbft.Locker.Unlock()

	header := block.GetHeader()
	ctxlog.Log("header", header)
	dbft.BlockManager.Insert(&block)

	status := dbft.BlockManager.GetBlockStatus(header.CaculateHash())
	ctxlog.Log("status", status)
	if status == blockchain.BLOCK_SAVED ||
		(status > blockchain.BLOCK_ERROR_START && status < blockchain.BLOCK_ERROR_END) ||
		status == blockchain.BLOCK_VOTED {
		//如果区块已经写入链中 or 是一个有问题的区块 or 已经投票成功 直接返回
		return
	}

	if status == blockchain.BLOCK_VALID {
		if lastVoteTime := dbft.BlockManager.GetVoteTime(block.GetHeader().Height); lastVoteTime+int64(blockchain.BackboneBlockInterval)/1e6 > time.Now().UnixNano()/1e6 {
			ctxlog.Log("Voted this height", true)
			return
		}
		dbft.BlockManager.SetVoteTime(block.GetHeader().Height, time.Now().UnixNano()/1e6)
		ctxlog.Log("SendVote", true)
		dbft.SendVote(*header)
		dbft.BlockManager.SetBlockStatus(header.CaculateHash(), blockchain.BLOCK_VOTED)
		return
	}

	// 判断此区块是否是一个interval之前打包的，如果是则放弃vote
	// unit： ms    单位：ms
	blockLatencyTime := int(time.Now().UnixNano()/1e6 - header.Timestamp) // 从节点打包到当前节点的延迟，单位ms
	blockInterval := int(blockchain.BackboneBlockInterval * 4 / 3)        // 当前链的打包间隔，单位nanoSecond,计算为ms
	if blockLatencyTime > blockInterval {
		ctxlog.Log("More than an interval", true)
		dbft.BlockManager.SetBlockStatus(header.CaculateHash(), blockchain.BLOCK_ERROR_BROADCAST_TIME)
		return
	}

	if !dbft.ValidatePackRight(header.Timestamp, dbft.Blockchain.LastHeader().Timestamp, block.Miner) {
		ctxlog.Log("Invalid node", true)
		return
	}

	transactions := block.GetTransactions()
	receipts := block.GetTxReceipts()
	ctxlog.Log("txs", transactions)
	ctxlog.Log("receipts", receipts)
	// 对区块进行validate和recover，如果区块数据没问题，则发送投票给其他节点
	if dbft.Blockchain.LastHeader().ValidateBlockStat(*header, transactions, receipts) {
		ctxlog.Log("SendVote", true)
		dbft.BlockManager.SetBlockStatus(header.CaculateHash(), blockchain.BLOCK_VOTED)
		dbft.SendVote(*header)
	} else {
		ctxlog.Log("error body", true)
		dbft.BlockManager.SetBlockStatus(header.CaculateHash(), blockchain.BLOCK_ERROR_BODY)
	}
}

// 校验从其他委托人节点来的区块成功之后发送投票
func (dbft DbftConsensus) SendVote(header blockchain.Header) {
	// 同一个节点在一个出块interval内对一个高度只会投票一次，所以先校验是否进行过投票
	//log.Info("Validating send vote interval.")
	// 获取上次投票时间 lastVoteTime < 0 表明当前区块没有投票过
	lastVoteTime := dbft.BlockManager.GetVoteTime(header.Height)
	if lastVoteTime > 0 {
		// 距离投票的毫秒数
		intervalInFact := int(time.Now().UnixNano()/1e6 - lastVoteTime)
		// 规则指定的毫秒数
		intervalInRule := int(blockchain.BackboneBlockInterval / 1e6)

		// 说明在一个intervalInRule内进行过投票
		if intervalInFact < intervalInRule {
			log.Info("This height has voted in paste interval, return.")
			return
		}
	}

	// 记录此次投票的时间
	dbft.BlockManager.SetVoteTime(header.Height, time.Now().UnixNano()/1e6)

	// 生成vote对象
	vote := &blockchain.PeerBlockVote{
		Vote: blockchain.BlockVoteDetail{
			BlockchainId: dbft.Blockchain.ChainId,
			BlockHash:    header.CaculateHash(),
			BlockHeight:  header.Height,
			VoteResult:   true,
		},
		Peer: conf.EKTConfig.Node,
	}

	// 签名
	err := vote.Sign(conf.EKTConfig.GetPrivateKey())
	if err != nil {
		log.Crit("Sign vote failed, recorded. %v", err)
		return
	}

	// 向其他节点发送签名后的vote信息
	log.Info("Signed this vote, sending vote result to other peers.")
	dbft.Client.SendVote(*vote)
	log.Info("Send vote to other peer succeed.")
}

// 委托人节点检测其他节点未按时出块的情况下， 当前节点进行打包的逻辑
func (dbft DbftConsensus) delegateRun() {
	log.Info("DBFT started.")

	//要求有半数以上节点存活才可以进行打包区块
	moreThanHalf := false
	for !moreThanHalf {
		if AliveDelegatePeerCount(param.MainChainDelegateNode, false) <= len(param.MainChainDelegateNode)/2 {
			log.Info("Alive node is less than half, waiting for other delegate node restart.")
			time.Sleep(3 * time.Second)
		} else {
			moreThanHalf = true
		}
	}

	// 每1/4个interval检测一次是否有漏块，如果发生漏块且当前节点可以出块，则进入打包流程
	interval := blockchain.BackboneBlockInterval / 4
	for {
		// 判断是否是当前节点打包区块
		if dbft.IsMyTurn() {
			log.Info("It is my turn")
			dbft.Client.SendHeartbeat()
			dbft.Pack()
		} else {
			log.Info("It is not my turn.")
		}

		time.Sleep(interval)
	}
}

func (dbft DbftConsensus) ReceiveHeartbeat(heartbeat types.Heartbeat) {
	now := time.Now().UnixNano() / 1e6
	if dbft.ValidatePackRight(now, dbft.Blockchain.LastHeader().Timestamp, heartbeat.Node) {
		dbft.Round.SetTime(now)
	}
}

func (dbft DbftConsensus) ValidatePackRight(packTime, lastBlockTime int64, node types.Peer) bool {
	round := dbft.GetRound()
	if lastBlockTime == 0 {
		return round.Peers[0].Equal(node)
	}
	intervalInFact, interval := int(packTime-lastBlockTime), int(blockchain.BackboneBlockInterval/1e6)

	// n表示距离上次打包的间隔
	n := int(intervalInFact) / int(interval)
	remainder := int(intervalInFact) % int(interval)
	if n == 0 {
		if remainder < int(interval)*3/2 {
			return round.Peers[(round.CurrentIndex+1)%round.Len()].Equal(node)
		} else {
			return false
		}
	}
	n++
	return round.Peers[(round.CurrentIndex+n)%round.Len()].Equal(node)
}

// 用于委托人线程判断当前节点是否有打包权限
func (dbft DbftConsensus) IsMyTurn() bool {
	now := time.Now().UnixNano() / 1e6
	if now-dbft.GetRound().GetTime() > (int64(blockchain.BackboneBlockInterval)/1e6)*4/3 {
		lastPackTime := dbft.Blockchain.LastHeader().Timestamp
		return dbft.ValidatePackRight(now, lastPackTime, conf.EKTConfig.Node)
	} else {
		return false
	}
}

// 开启delegate线程
func (dbft *DbftConsensus) Run() {
	// 稳定启动dbft.delegateRun()
	go func() {
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.PrintStack("dbft.run.delegateRun")
					}
				}()
				dbft.delegateRun()
			}()
		}
	}()

	// 稳定启动dbft.delegateSync()
	go func() {
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.PrintStack("dbft.run.delegateSync")
					}
				}()
				dbft.delegateSync()
			}()
		}
	}()
}

// delegateSync同步主要是监控在一定interval如果height没有被委托人间投票改变，则通过height进行同步
func (dbft *DbftConsensus) delegateSync() {
	lastHeight := dbft.Blockchain.GetLastHeight()
	for {
		height := dbft.Blockchain.GetLastHeight()
		if height == lastHeight {
			log.Debug("Height has not change for an interval, synchronizing block.")
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.PrintStack("dbft.delegateSync")
					}
				}()
				if dbft.SyncHeight(lastHeight + 1) {
					log.Debug("Synchronized block at lastHeight %d.", lastHeight+1)
					lastHeight = dbft.Blockchain.GetLastHeight()
				} else {
					log.Debug("Synchronize block at lastHeight %d failed.", lastHeight+1)
					time.Sleep(blockchain.BackboneBlockInterval)
				}
			}()
		}

		lastHeight = dbft.Blockchain.GetLastHeight()
	}
}

// 进行下一个区块的打包
func (dbft DbftConsensus) Pack() {
	lastHeader := dbft.Blockchain.LastHeader()
	dbft.Locker.Lock()
	defer dbft.Locker.Unlock()
	if dbft.BlockManager.CheckHeightInterval(lastHeader.Height+1, int64(blockchain.BackboneBlockInterval)) {
		dbft.BlockManager.SetBlockStatusByHeight(lastHeader.Height+1, time.Now().UnixNano()/1e6)
	} else {
		return
	}

	clog := ctxlog.NewContextLog("pack block")
	defer clog.Finish()

	block := blockchain.CreateBlock(lastHeader, conf.EKTConfig.Node)
	dbft.Blockchain.PackTransaction(clog, block)

	// 增加打包信息
	dbft.SaveHeader(*block.GetHeader())
	dbft.BlockManager.Insert(block)
	dbft.BlockManager.SetBlockStatus(block.Hash, blockchain.BLOCK_VALID)
	dbft.BlockManager.SetBlockStatusByHeight(block.GetHeader().Height, block.GetHeader().Timestamp)

	// 签名
	if err := block.Sign(conf.EKTConfig.PrivateKey); err != nil {
		log.Crit("Sign block failed. %v", err)
	} else {
		// 广播
		dbft.Client.BroadcastBlock(*block)
		clog.Log("block", block)
	}
}

// 从db中recover数据
func (dbft DbftConsensus) RecoverFromDB() {
	header := encapdb.GetLastHeader(dbft.Blockchain.ChainId)
	// 如果是第一次打开
	if header == nil {
		// 将创世块写入数据库
		accounts := conf.EKTConfig.GenesisBlockAccounts
		block := blockchain.CreateGenesisBlock(accounts)
		header = block.GetHeader()
		dbft.SaveBlock(&block, nil)
	}
	dbft.Blockchain.SetLastHeader(*header)
	log.Info("Recovered from local database.")
}

// 获取存活的委托人节点数量
func AliveDelegatePeerCount(peers types.Peers, print bool) int {
	count := 0
	for _, peer := range peers {
		if peer.IsAlive() {
			if print {
				log.Info("Peer %s is alive, address: %s \n", peer.Account, peer.Address)
			}
			count++
		}
	}
	return count
}

// 根据height同步区块
func (dbft DbftConsensus) SyncHeight(height int64) bool {
	log.Info("Synchronizing block at height %d \n", height)
	if dbft.Blockchain.GetLastHeight() >= height {
		return true
	}
	block := dbft.Client.GetBlockByHeight(height)
	if block == nil {
		return false
	}
	header := block.GetHeader()
	if header == nil || header.Height != height {
		return false
	} else {
		votes := dbft.Client.GetVotesByBlockHash(hex.EncodeToString(header.CaculateHash()))
		if votes == nil || !votes.Validate() {
			return false
		}
		transactions := block.GetTransactions()
		receipts := block.GetTxReceipts()
		last := dbft.Blockchain.LastHeader()
		if last.ValidateBlockStat(*header, transactions, receipts) {
			dbft.SaveBlock(block, votes)
		}
	}
	return false
}

// 从其他委托人节点发过来的区块的投票进行记录
func (dbft DbftConsensus) VoteFromPeer(vote blockchain.PeerBlockVote) {
	dbft.VoteResults.Insert(vote)

	if dbft.VoteResults.Number(vote.Vote.BlockHash) > len(dbft.GetRound().Peers)/2 {
		log.Info("BlockVoteDetail number more than half node, sending vote result to other nodes.")
		votes := dbft.VoteResults.GetVoteResults(hex.EncodeToString(vote.Vote.BlockHash))
		dbft.Client.SendVoteResult(votes)
	}
}

// 收到从其他节点发送过来的voteResult，校验之后可以写入到区块链中
func (dbft DbftConsensus) RecieveVoteResult(votes blockchain.Votes) bool {
	if !dbft.ValidateVotes(votes) {
		log.Info("Votes validate failed. %v", votes)
		return false
	}

	status := dbft.BlockManager.GetBlockStatus(votes[0].Vote.BlockHash)

	// 已经写入到链中
	if status == blockchain.BLOCK_SAVED {
		return true
	}

	// 未同步区块body
	if status > blockchain.BLOCK_ERROR_START && status < blockchain.BLOCK_ERROR_END {
		// 未同步区块体通过sync同步区块
		log.Crit("Invalid block and votes, block.hash = %s", hex.EncodeToString(votes[0].Vote.BlockHash))
	}

	// 区块已经校验但未写入链中
	if status == blockchain.BLOCK_VALID || status == blockchain.BLOCK_VOTED {
		block := dbft.BlockManager.GetBlock(votes[0].Vote.BlockHash)
		dbft.SaveBlock(block, votes)
		round := dbft.GetRound()
		if round.Peers[(round.CurrentIndex+1)%round.Len()].Equal(conf.EKTConfig.Node) {
			dbft.Pack()
		}
		return true
	}

	return false
}

func (dbft DbftConsensus) SaveBlock(block *blockchain.Block, votes blockchain.Votes) {
	header := *block.GetHeader()
	dbft.Round.UpdateIndex(block.Miner.Account)
	encapdb.SetVoteResults(dbft.Blockchain.ChainId, hex.EncodeToString(block.Hash), votes)
	encapdb.SetBlockByHeight(dbft.Blockchain.ChainId, header.Height, *block)
	encapdb.SetHeaderByHeight(dbft.Blockchain.ChainId, header.Height, header)
	encapdb.SetLastHeader(dbft.Blockchain.ChainId, header)
	dbft.Blockchain.SetLastHeader(header)
	dbft.Blockchain.NotifyPool(block.GetTransactions())
}

func (dbft DbftConsensus) SaveHeader(header blockchain.Header) {
	db.GetDBInst().Set(header.CaculateHash(), header.Bytes())
}

// 校验voteResults
func (dbft DbftConsensus) ValidateVotes(votes blockchain.Votes) bool {
	if !votes.Validate() {
		return false
	}

	if votes.Len() <= dbft.GetRound().Len()/2 {
		return false
	}
	return true
}
