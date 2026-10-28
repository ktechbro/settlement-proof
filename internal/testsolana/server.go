// Package testsolana supplies synthetic Solana RPC history, never a live validator.
package testsolana

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/ktechbro/settlement-proof/internal/solana"
)

func Key(seed byte) string       { return solana.Encode58(bytes.Repeat([]byte{seed}, 32)) }
func Signature(seed byte) string { return solana.Encode58(bytes.Repeat([]byte{seed}, 64)) }

var Source = Key(2)
var Destination = Key(3)
var Buyer = Key(4)
var Seller = Key(5)
var Genesis = Key(99)

func balance(index int, owner string) map[string]any {
	return map[string]any{"accountIndex": index, "mint": solana.USDCMint, "owner": owner, "programId": solana.TokenProgram, "uiTokenAmount": map[string]any{"amount": "1000000000", "decimals": 6}}
}
func parsed(amount string, checked bool) map[string]any {
	info := map[string]any{"source": Source, "destination": Destination, "authority": Buyer}
	kind := "transfer"
	if checked {
		kind = "transferChecked"
		info["mint"] = solana.USDCMint
		info["tokenAmount"] = map[string]any{"amount": amount, "decimals": 6}
	} else {
		info["amount"] = amount
	}
	return map[string]any{"program": "spl-token", "programId": solana.TokenProgram, "parsed": map[string]any{"type": kind, "info": info}, "stackHeight": 1}
}

func Transaction(seed byte, amounts []string, inner bool) map[string]any {
	keys := []any{}
	for _, key := range []string{Buyer, Source, Destination, solana.USDCMint, solana.TokenProgram, Key(7)} {
		keys = append(keys, map[string]any{"pubkey": key, "source": "transaction", "signer": false, "writable": true})
	}
	instructions := []any{}
	for i, amount := range amounts {
		instructions = append(instructions, parsed(amount, i%2 == 1))
	}
	outer := instructions
	groups := []any{}
	if inner {
		outer = []any{map[string]any{"programId": Key(7), "accounts": []string{Source, Destination}, "data": "1"}}
		groups = append(groups, map[string]any{"index": 0, "instructions": instructions})
	}
	balances := []any{balance(1, Buyer), balance(2, Seller)}
	return map[string]any{"transaction": map[string]any{"signatures": []string{Signature(seed)}, "message": map[string]any{"accountKeys": keys, "instructions": outer}}, "meta": map[string]any{"err": nil, "preTokenBalances": balances, "postTokenBalances": balances, "innerInstructions": groups, "logMessages": []string{"Program " + solana.TokenProgram + " invoke [1]", "Program " + solana.TokenProgram + " success"}}, "version": "legacy"}
}

func VersionedTransaction(seed byte, amount uint64) map[string]any {
	data := make([]byte, 10)
	data[0] = 12
	binary.LittleEndian.PutUint64(data[1:9], amount)
	data[9] = 6
	balances := []any{balance(2, Buyer), balance(3, Seller)}
	return map[string]any{"version": 0, "transaction": map[string]any{"signatures": []string{Signature(seed)}, "message": map[string]any{"accountKeys": []string{Buyer, solana.TokenProgram}, "addressTableLookups": []any{map[string]any{"accountKey": Key(8), "writableIndexes": []int{0, 1}, "readonlyIndexes": []int{2}}}, "instructions": []any{map[string]any{"programIdIndex": 1, "accounts": []int{2, 4, 3, 0}, "data": solana.Encode58(data)}}}}, "meta": map[string]any{"err": nil, "loadedAddresses": map[string]any{"writable": []string{Source, Destination}, "readonly": []string{solana.USDCMint}}, "preTokenBalances": balances, "postTokenBalances": balances, "innerInstructions": []any{}, "logMessages": []string{"Program " + solana.TokenProgram + " success"}}}
}

func Block(slot, parent int64, txs ...map[string]any) solana.Block {
	raw, _ := json.Marshal(txs)
	if len(txs) == 0 {
		raw = []byte("[]")
	}
	at := int64(1700000000) + slot
	return solana.Block{Hash: Key(byte(slot)), ParentHash: Key(byte(parent)), ParentSlot: &parent, Time: &at, Transactions: raw}
}

func V1Transaction(seed byte, amounts []string) map[string]any {
	tx := Transaction(seed, amounts, false)
	tx["version"] = 1
	tx["transaction"].(map[string]any)["message"].(map[string]any)["transactionConfig"] = map[string]any{"computeUnitLimit": 30000, "loadedAccountsDataSizeLimit": 200000, "heapSize": nil, "priorityFee": nil}
	return tx
}

type Server struct {
	Mu             sync.Mutex
	Root           int64
	Blocks         map[int64]solana.Block
	FullCalls      int
	FailFullAfter  int
	FirstAvailable int64
	HiddenSlot     int64
	ConflictSlot   int64
	WrongGenesis   bool
	NullFullSlot   int64
}

func New() *Server {
	failed := Transaction(50, []string{"12345"}, false)
	failed["meta"].(map[string]any)["err"] = map[string]any{"InstructionError": []any{0, "Custom"}}
	return &Server{Root: 109, FirstAvailable: 99, HiddenSlot: -1, ConflictSlot: -1, NullFullSlot: -1, Blocks: map[int64]solana.Block{
		100: Block(100, 99), 101: Block(101, 100, V1Transaction(11, []string{"50000000", "7000000"})), 103: Block(103, 101, Transaction(12, []string{"9000000", "1"}, true)), 108: Block(108, 103, VersionedTransaction(13, 11000000), failed), 109: Block(109, 108), 110: Block(110, 109, Transaction(14, []string{"999000000"}, false)),
	}}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	var request struct {
		ID     int64             `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		http.Error(w, "malformed request", 400)
		return
	}
	var value any
	var rpcErr any
	switch request.Method {
	case "getGenesisHash":
		value = Genesis
		if s.WrongGenesis {
			value = Key(98)
		}
	case "getFirstAvailableBlock":
		value = s.FirstAvailable
	case "getSlot":
		value = s.Root
	case "getBlocks", "getBlocksWithLimit":
		var from, to int64
		json.Unmarshal(request.Params[0], &from)
		json.Unmarshal(request.Params[1], &to)
		limit := int64(0)
		if request.Method == "getBlocksWithLimit" {
			limit = to
			to = s.Root
		}
		list := []int64{}
		for slot := range s.Blocks {
			if slot >= from && slot <= to && slot <= s.Root && slot != s.HiddenSlot {
				list = append(list, slot)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		if limit > 0 && int64(len(list)) > limit {
			list = list[:limit]
		}
		value = list
	case "getBlock":
		var slot int64
		json.Unmarshal(request.Params[0], &slot)
		var config struct {
			Details    string `json:"transactionDetails"`
			Commitment string `json:"commitment"`
			MaxVersion *int   `json:"maxSupportedTransactionVersion"`
		}
		json.Unmarshal(request.Params[1], &config)
		if config.Commitment != "finalized" || config.MaxVersion == nil || *config.MaxVersion != 1 {
			http.Error(w, "finalized commitment required", 400)
			return
		}
		block, ok := s.Blocks[slot]
		if !ok {
			break
		}
		if config.Details == "full" {
			s.FullCalls++
			if s.FailFullAfter > 0 && s.FullCalls > s.FailFullAfter {
				http.Error(w, "injected provider failure", 503)
				return
			}
			if s.NullFullSlot == slot {
				break
			}
		} else {
			block.Transactions = nil
		}
		if slot == s.ConflictSlot {
			block.Hash = Key(200)
		}
		value = block
	default:
		rpcErr = map[string]any{"code": -32601, "message": fmt.Sprint("unknown method ", request.Method)}
	}
	envelope := map[string]any{"jsonrpc": "2.0", "id": request.ID}
	if rpcErr != nil {
		envelope["error"] = rpcErr
	} else {
		envelope["result"] = value
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(envelope)
}
