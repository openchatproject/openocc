package api

import (
	"encoding/json"
	"github.com/OpenOCC/OCC/node"

	"github.com/OpenOCC/OCC/blockchain"
	"github.com/OpenOCC/xserver/x_err"
	"github.com/OpenOCC/xserver/x_http/x_req"
	"github.com/OpenOCC/xserver/x_http/x_resp"
	"github.com/OpenOCC/xserver/x_http/x_router"
)

func init() {
	x_router.Post("/vote/api/vote", voteBlock)
	x_router.Post("/vote/api/voteResult", voteResult)
	x_router.Get("/vote/api/getVotes", getVotes)
}

func voteBlock(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	var vote blockchain.PeerBlockVote
	err := json.Unmarshal(req.Body, &vote)
	if err != nil {
		return x_resp.Return(nil, err)
	}
	if !vote.Validate() {
		return x_resp.Return(false, nil)
	}
	node.VoteFromPeer(vote)
	return nil, nil
}

func voteResult(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	var votes blockchain.Votes
	err := json.Unmarshal(req.Body, &votes)
	if err != nil {
		return x_resp.Return(nil, err)
	}
	go node.VoteResultFromPeer(votes)
	return x_resp.Success(make(map[string]interface{})), nil
}

func getVotes(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	blockHash := req.MustGetString("hash")
	votes := node.GetVoteResults(1, blockHash)
	return x_resp.Return(votes, nil)
}
