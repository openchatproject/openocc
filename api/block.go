package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/OpenOCC/OCC/blockchain"
	"github.com/OpenOCC/OCC/encapdb"
	"github.com/OpenOCC/OCC/node"
	"github.com/OpenOCC/xserver/x_err"
	"github.com/OpenOCC/xserver/x_http/x_req"
	"github.com/OpenOCC/xserver/x_http/x_resp"
	"github.com/OpenOCC/xserver/x_http/x_router"
)

func init() {
	x_router.Post("/block/api/last", lastBlock)
	x_router.Get("/block/api/getHeaderByHeight", getHeaderByHeight)
	x_router.Get("/block/api/getHeaderByHash", getHeaderByHash)
	x_router.Get("/block/api/getBlockByHeight", getBlockByHeight)
	x_router.Post("/block/api/blockFromPeer", broadcast, blockFromPeer)
}

func getBlockByHeight(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	height := req.MustGetInt64("height")
	block := encapdb.GetBlockByHeight(1, height)
	if block == nil {
		return x_resp.Fail(-1, "not found", nil), nil
	}
	return &x_resp.XRespContainer{
		HttpCode: 200,
		Body:     block.Bytes(),
	}, nil
}

func getHeaderByHash(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	hash := req.MustGetString("hash")
	h, err := hex.DecodeString(hash)
	if err != nil {
		return x_resp.Return(nil, err)
	}
	return x_resp.Return(encapdb.GetHeaderByHash(h), nil)
}

func lastBlock(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	return x_resp.Return(node.GetMainChain().LastHeader(), nil)
}

func getHeaderByHeight(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	bc := node.GetMainChain()
	height := req.MustGetInt64("height")
	if bc.GetLastHeight() < height {
		return nil, x_err.New(-404, fmt.Sprintf("Heigth %d is heigher than current height, current height is %d \n ", height, bc.GetLastHeight()))
	}
	return x_resp.Return(node.GetBlockByHeight(1, height), nil)
}

func blockFromPeer(req *x_req.XReq) (*x_resp.XRespContainer, *x_err.XErr) {
	var block blockchain.Block
	json.Unmarshal(req.Body, &block)
	lastHeight := node.GetMainChain().GetLastHeight()
	if lastHeight+1 != block.GetHeader().Height {
		return x_resp.Fail(-1, "error invalid height", nil), nil
	}
	node.BlockFromPeer(block)
	return x_resp.Return("recieved", nil)
}
