package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

var ErrConflict = errors.New("finalized history conflict")
var ErrStale = errors.New("checkpoint changed; retry")

const TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type Header struct {
	Number     string `json:"number"`
	Hash       string `json:"hash"`
	ParentHash string `json:"parentHash"`
	Timestamp  string `json:"timestamp"`
}

type Log struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	BlockHash        string   `json:"blockHash"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

type Settlement struct {
	ID              int64           `json:"id"`
	Chain           string          `json:"chain"`
	Asset           string          `json:"asset"`
	TransactionHash string          `json:"transaction_hash"`
	LogIndex        int64           `json:"log_index"`
	BlockNumber     int64           `json:"block_number"`
	BlockHash       string          `json:"block_hash"`
	BlockTime       int64           `json:"block_time"`
	Sender          string          `json:"sender"`
	Recipient       string          `json:"recipient"`
	Amount          string          `json:"amount_atomic"`
	Provider        string          `json:"provider"`
	FinalizedNumber int64           `json:"finalized_number"`
	FinalizedHash   string          `json:"finalized_hash"`
	Raw             json.RawMessage `json:"raw_log"`
}

type Checkpoint struct {
	Height   int64  `json:"height"`
	Hash     string `json:"hash"`
	Revision int64  `json:"revision"`
	Halted   bool   `json:"halted"`
}

type Batch struct {
	From, To    int64
	End         Header
	Finalized   Header
	Headers     []Header
	Settlements []Settlement
	Provider    string
}

func Quantity(s string) (int64, error) {
	if !strings.HasPrefix(s, "0x") || len(s) < 3 {
		return 0, fmt.Errorf("invalid hex quantity")
	}
	n := new(big.Int)
	if _, ok := n.SetString(s[2:], 16); !ok || !n.IsInt64() || n.Sign() < 0 {
		return 0, fmt.Errorf("quantity outside supported signed 64-bit range")
	}
	return n.Int64(), nil
}

func validHex(s string, bytes int) bool {
	if len(s) != 2+2*bytes || !strings.HasPrefix(s, "0x") {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

func decodeLog(l Log, h Header, cfg Config, provider string, final Header) (Settlement, error) {
	var s Settlement
	if l.Removed || !strings.EqualFold(l.Address, cfg.Token) || len(l.Topics) != 3 || strings.ToLower(l.Topics[0]) != TransferTopic {
		return s, fmt.Errorf("invalid transfer envelope")
	}
	for _, topic := range l.Topics[1:] {
		if !validHex(topic, 32) || topic[2:26] != strings.Repeat("0", 24) {
			return s, fmt.Errorf("invalid address topic")
		}
	}
	if !validHex(l.Data, 32) || !validHex(l.TransactionHash, 32) || !strings.EqualFold(l.BlockHash, h.Hash) {
		return s, fmt.Errorf("invalid transfer identity/data")
	}
	bn, err := Quantity(l.BlockNumber)
	if err != nil {
		return s, err
	}
	hn, err := Quantity(h.Number)
	if err != nil || bn != hn {
		return s, fmt.Errorf("log/header number mismatch")
	}
	li, err := Quantity(l.LogIndex)
	if err != nil {
		return s, err
	}
	if _, err = Quantity(l.TransactionIndex); err != nil {
		return s, err
	}
	bt, err := Quantity(h.Timestamp)
	if err != nil {
		return s, err
	}
	fn, err := Quantity(final.Number)
	if err != nil {
		return s, err
	}
	amount, _ := new(big.Int).SetString(l.Data[2:], 16)
	raw, err := json.Marshal(l)
	if err != nil {
		return s, err
	}
	return Settlement{Chain: cfg.Chain, Asset: cfg.Token, TransactionHash: strings.ToLower(l.TransactionHash), LogIndex: li, BlockNumber: bn, BlockHash: strings.ToLower(l.BlockHash), BlockTime: bt, Sender: "0x" + strings.ToLower(l.Topics[1][26:]), Recipient: "0x" + strings.ToLower(l.Topics[2][26:]), Amount: amount.String(), Provider: provider, FinalizedNumber: fn, FinalizedHash: strings.ToLower(final.Hash), Raw: raw}, nil
}

// Scope isolates checkpoints when the watched addresses, token, or start change.
func (c Config) Scope() string {
	w := append([]string(nil), c.Watch...)
	sort.Strings(w)
	b, _ := json.Marshal([]any{"evm-finalized-v1", c.Chain, c.ChainID, c.Token, c.Start, c.AnchorNumber, c.AnchorHash, w})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
