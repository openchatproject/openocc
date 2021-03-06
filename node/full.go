package node

import (
	"github.com/OpenOCC/OCC/blockchain"
	"github.com/OpenOCC/OCC/conf"
	"github.com/OpenOCC/OCC/consensus"
	"github.com/OpenOCC/OCC/core/types"
	"github.com/OpenOCC/OCC/occclient"
	"github.com/OpenOCC/OCC/encapdb"
	"github.com/OpenOCC/OCC/param"
	"time"
)

type FullNode struct {
	config     conf.EKTConf
	blockchain *blockchain.BlockChain
	dbft       *consensus.DbftConsensus
	client     occclient.IClient
}

func NewFullMode(config conf.EKTConf) *FullNode {
	node := &FullNode{
		config:     config,
		blockchain: blockchain.NewBlockChain(1),
		client:     occclient.NewClient(param.MainChainDelegateNode),
	}
	node.dbft = consensus.NewDbftConsensus(node.blockchain, node.client)
	return node
}

func (node FullNode) StartNode() {
	node.recoverFromDB()
	go node.loop()
}

func (node FullNode) GetBlockChain() *blockchain.BlockChain {
	return node.blockchain
}

func (node FullNode) Heartbeat(heartbeat types.Heartbeat) {
}

func (node FullNode) recoverFromDB() {
	node.dbft.RecoverFromDB()
}

func (node FullNode) BlockFromPeer(block blockchain.Block) {
	return
}

func (node FullNode) VoteFromPeer(vote blockchain.PeerBlockVote) {
	return
}

func (node FullNode) VoteResultFromPeer(votes blockchain.Votes) {
	return
}

func (node FullNode) GetVoteResults(chainId int64, hash string) blockchain.Votes {
	return encapdb.GetVoteResults(chainId, hash)
}

func (node FullNode) GetBlockByHeight(chainId, height int64) *blockchain.Block {
	return encapdb.GetBlockByHeight(chainId, height)
}

func (node FullNode) GetHeaderByHeight(chainId, height int64) *blockchain.Header {
	return encapdb.GetHeaderByHeight(chainId, height)
}

func (node FullNode) loop() {
	fail, failTime := false, 0

	for height := node.blockchain.GetLastHeight() + 1; ; {
		if fail {
			if failTime >= 3 {
				time.Sleep(blockchain.BackboneBlockInterval)
			}
		}

		if node.dbft.SyncHeight(height) {
			height++
			fail, failTime = false, 0
		} else {
			fail, failTime = true, failTime+1
		}
	}
}
