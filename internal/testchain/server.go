// Package testchain provides an explicitly synthetic EVM RPC fixture, not a node.
package testchain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/ktechbro/settlement-proof/internal/indexer"
)

const Token = "0x1111111111111111111111111111111111111111"
const Buyer = "0x2222222222222222222222222222222222222222"
const Seller = "0x3333333333333333333333333333333333333333"

func Hash(n int64) string { return fmt.Sprintf("0x%064x", n+1000) }
func Header(n int64) indexer.Header {
	return indexer.Header{Number: fmt.Sprintf("0x%x", n), Hash: Hash(n), ParentHash: Hash(n - 1), Timestamp: fmt.Sprintf("0x%x", 1700000000+n)}
}
func Transfer(block, tx, logIndex int64, amount string) indexer.Log {
	n, _ := new(big.Int).SetString(amount, 10)
	return indexer.Log{Address: Token, Topics: []string{indexer.TransferTopic, "0x" + strings.Repeat("0", 24) + Buyer[2:], "0x" + strings.Repeat("0", 24) + Seller[2:]}, Data: fmt.Sprintf("0x%064x", n), BlockNumber: fmt.Sprintf("0x%x", block), BlockHash: Hash(block), TransactionHash: Hash(tx + 100), TransactionIndex: "0x0", LogIndex: fmt.Sprintf("0x%x", logIndex)}
}

type Server struct {
	Mu             sync.Mutex
	Final          int64
	Logs           []indexer.Log
	FailLogsAfter  int
	LogCalls       int
	Duplicates     bool
	ConflictHeight int64
	RangeLimit     int64
	NullFinal      bool
	FinalHash      string
}

func New() *Server {
	return &Server{Final: 5, Logs: []indexer.Log{Transfer(1, 1, 0, "50000000"), Transfer(2, 2, 0, "7000000"), Transfer(2, 2, 1, "9000000"), Transfer(4, 4, 0, "1"), Transfer(6, 6, 0, "999000000")}, ConflictHeight: -1}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	var req struct {
		ID     int64             `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	var result any
	var rpcErr any
	switch req.Method {
	case "eth_chainId":
		result = "0x2105"
	case "eth_getBlockByNumber":
		var tag string
		_ = json.Unmarshal(req.Params[0], &tag)
		n := s.Final
		if tag != "finalized" {
			n, _ = strconv.ParseInt(strings.TrimPrefix(tag, "0x"), 16, 64)
		}
		h := Header(n)
		if tag == "finalized" && s.FinalHash != "" {
			h.Hash = s.FinalHash
		}
		if n == s.ConflictHeight {
			h.Hash = Hash(n + 900)
		}
		if !(s.NullFinal && tag == "finalized") {
			result = h
		}
	case "eth_getLogs":
		s.LogCalls++
		if s.FailLogsAfter > 0 && s.LogCalls > s.FailLogsAfter {
			http.Error(w, "injected outage", 503)
			return
		}
		var q struct {
			From string `json:"fromBlock"`
			To   string `json:"toBlock"`
		}
		_ = json.Unmarshal(req.Params[0], &q)
		from, _ := strconv.ParseInt(strings.TrimPrefix(q.From, "0x"), 16, 64)
		to, _ := strconv.ParseInt(strings.TrimPrefix(q.To, "0x"), 16, 64)
		if s.RangeLimit > 0 && to-from+1 > s.RangeLimit {
			rpcErr = map[string]any{"code": -32005, "message": "range limit"}
			break
		}
		values := []indexer.Log{}
		for _, l := range s.Logs {
			n, _ := indexer.Quantity(l.BlockNumber)
			if n >= from && n <= to {
				values = append(values, l)
				if s.Duplicates {
					values = append(values, l)
				}
			}
		}
		result = values
	default:
		rpcErr = map[string]any{"code": -32601, "message": "unsupported fixture method"}
	}
	w.Header().Set("Content-Type", "application/json")
	envelope := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		envelope["error"] = rpcErr
	} else {
		envelope["result"] = result
	}
	_ = json.NewEncoder(w).Encode(envelope)
}
