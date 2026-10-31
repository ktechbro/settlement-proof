package solana

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EscrowEvent is the observed signed-verdict-escrow-v1 instruction and its actual token effect.
// Config is absent on refunds because the refund instruction does not supply it.
type EscrowEvent struct {
	ID               int64           `json:"id"`
	EventKey         string          `json:"event_key"`
	Chain            string          `json:"chain"`
	Program          string          `json:"program"`
	Escrow           string          `json:"escrow"`
	Config           string          `json:"config,omitempty"`
	DealID           string          `json:"deal_id,omitempty"`
	Kind             string          `json:"kind"`
	Mint             string          `json:"mint"`
	Vault            string          `json:"vault"`
	Buyer            string          `json:"buyer,omitempty"`
	Seller           string          `json:"seller,omitempty"`
	Amount           string          `json:"amount_atomic"`
	RefundAt         int64           `json:"refund_at,omitempty"`
	ExpiresAt        int64           `json:"expires_at,omitempty"`
	Signature        string          `json:"transaction_hash"`
	Outer            int             `json:"outer_instruction"`
	Inner            int             `json:"inner_instruction"`
	LogIndex         int64           `json:"log_index"`
	Slot             int64           `json:"slot"`
	TransactionIndex int             `json:"transaction_index"`
	BlockHash        string          `json:"block_hash"`
	BlockTime        int64           `json:"block_time"`
	FinalizedSlot    int64           `json:"finalized_number"`
	FinalizedHash    string          `json:"finalized_hash"`
	SettlementID     int64           `json:"settlement_id"`
	Raw              json.RawMessage `json:"evidence"`
}

func (e EscrowEvent) Key() string {
	raw, _ := json.Marshal([]any{e.Chain, e.Program, e.Signature, e.Outer})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (e EscrowEvent) Fingerprint() string {
	e.ID = 0
	e.EventKey = ""
	e.SettlementID = 0
	e.FinalizedSlot = 0
	e.FinalizedHash = ""
	e.Raw = nil
	raw, _ := json.Marshal(e)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func Discriminator(namespace, name string) []byte {
	h := sha256.Sum256([]byte(namespace + ":" + name))
	return append([]byte(nil), h[:8]...)
}

type escrowInstruction struct {
	event               EscrowEvent
	source, destination string
	skip                bool
}

func programID(in Instruction, keys []string) (string, error) {
	if in.ProgramIndex != nil {
		if *in.ProgramIndex < 0 || *in.ProgramIndex >= len(keys) {
			return "", fmt.Errorf("program index out of range")
		}
		return keys[*in.ProgramIndex], nil
	}
	return in.ProgramID, nil
}
func instructionAccounts(in Instruction, keys []string) ([]string, error) {
	out := []string{}
	for _, raw := range in.Accounts {
		var key string
		if json.Unmarshal(raw, &key) != nil {
			var i int
			if json.Unmarshal(raw, &i) != nil || i < 0 || i >= len(keys) {
				return nil, fmt.Errorf("escrow account index out of range")
			}
			key = keys[i]
		}
		if !Valid58(key, 32) {
			return nil, fmt.Errorf("invalid escrow account")
		}
		out = append(out, key)
	}
	return out, nil
}

func parseEscrow(in Instruction, keys []string, mint string) (escrowInstruction, error) {
	var result escrowInstruction
	program, err := programID(in, keys)
	if err != nil {
		return result, err
	}
	result.event.Program = program
	result.event.Mint = mint
	data, err := Decode58(in.Data)
	if err != nil || len(data) < 8 {
		return result, fmt.Errorf("missing escrow instruction bytes")
	}
	if bytes.Equal(data[:8], Discriminator("global", "create_config")) {
		result.skip = true
		return result, nil
	}
	accounts, err := instructionAccounts(in, keys)
	if err != nil {
		return result, err
	}
	switch {
	case bytes.Equal(data[:8], Discriminator("global", "deposit")):
		if len(data) != 64 || len(accounts) != 8 || accounts[5] != mint || accounts[6] != TokenProgram {
			return result, fmt.Errorf("invalid deposit ABI or mint/program")
		}
		e := &result.event
		e.Kind = "funding"
		e.Buyer = accounts[0]
		e.Config = accounts[1]
		e.Escrow = accounts[2]
		e.Vault = accounts[3]
		e.DealID = strconv.FormatUint(binary.LittleEndian.Uint64(data[8:16]), 10)
		e.Amount = strconv.FormatUint(binary.LittleEndian.Uint64(data[16:24]), 10)
		e.RefundAt = int64(binary.LittleEndian.Uint64(data[24:32]))
		e.Seller = Encode58(data[32:64])
		result.source = accounts[4]
		result.destination = e.Vault
		if e.Amount == "0" || e.RefundAt <= 0 {
			return result, fmt.Errorf("invalid deposit terms")
		}
	case bytes.Equal(data[:8], Discriminator("global", "settle")):
		if len(data) != 16 || len(accounts) != 7 || accounts[4] != mint || accounts[5] != TokenProgram {
			return result, fmt.Errorf("invalid release ABI or mint/program")
		}
		e := &result.event
		e.Kind = "release"
		e.Config = accounts[0]
		e.Escrow = accounts[1]
		e.Vault = accounts[2]
		e.ExpiresAt = int64(binary.LittleEndian.Uint64(data[8:]))
		result.source = e.Vault
		result.destination = accounts[3]
	case bytes.Equal(data[:8], Discriminator("global", "refund")):
		if len(data) != 8 || len(accounts) != 5 || accounts[3] != mint || accounts[4] != TokenProgram {
			return result, fmt.Errorf("invalid refund ABI or mint/program")
		}
		e := &result.event
		e.Kind = "refund"
		e.Escrow = accounts[0]
		e.Vault = accounts[1]
		result.source = e.Vault
		result.destination = accounts[2]
	default:
		return result, fmt.Errorf("unsupported registered escrow instruction")
	}
	return result, nil
}

// ownedLogs authenticates Program data by the runtime invocation stack, not by text inside a log.
func ownedLogs(tx Transaction, keys []string, allowed map[string]bool) (map[int][][]byte, error) {
	out := map[int][][]byte{}
	if tx.Meta == nil || tx.Meta.Logs == nil {
		return nil, fmt.Errorf("missing escrow execution logs")
	}
	queue := []int{}
	for i, in := range tx.Transaction.Message.Instructions {
		p, err := programID(in, keys)
		if err != nil {
			return nil, err
		}
		if allowed[p] {
			queue = append(queue, i)
		}
	}
	type frame struct {
		program string
		outer   int
	}
	stack := []frame{}
	next := 0
	for _, line := range *tx.Meta.Logs {
		if strings.Contains(strings.ToLower(line), "log truncated") {
			return nil, fmt.Errorf("truncated escrow logs")
		}
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[0] == "Program" && fields[2] == "invoke" {
			depth, err := strconv.Atoi(strings.Trim(fields[3], "[]"))
			if err != nil || depth != len(stack)+1 {
				return nil, fmt.Errorf("inconsistent invocation stack")
			}
			outer := -1
			if len(stack) > 0 {
				outer = stack[0].outer
			}
			if allowed[fields[1]] {
				if depth != 1 {
					return nil, fmt.Errorf("nested escrow invocation unsupported")
				}
				if next >= len(queue) {
					return nil, fmt.Errorf("unexpected escrow invocation")
				}
				outer = queue[next]
				next++
				expected, _ := programID(tx.Transaction.Message.Instructions[outer], keys)
				if expected != fields[1] {
					return nil, fmt.Errorf("escrow invocation order mismatch")
				}
			}
			stack = append(stack, frame{fields[1], outer})
			continue
		}
		if len(fields) >= 3 && fields[0] == "Program" && (fields[2] == "success" || strings.HasPrefix(fields[2], "failed")) {
			if len(stack) == 0 || stack[len(stack)-1].program != fields[1] {
				return nil, fmt.Errorf("inconsistent invocation completion")
			}
			if strings.HasPrefix(fields[2], "failed") && allowed[fields[1]] {
				return nil, fmt.Errorf("failed escrow invocation")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if strings.HasPrefix(line, "Program data: ") && len(stack) > 0 && allowed[stack[len(stack)-1].program] {
			f := stack[len(stack)-1]
			data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "Program data: "))
			if err != nil {
				return nil, fmt.Errorf("invalid escrow event encoding")
			}
			out[f.outer] = append(out[f.outer], data)
		}
	}
	if len(stack) != 0 || next != len(queue) {
		return nil, fmt.Errorf("incomplete escrow invocation logs")
	}
	return out, nil
}

func DecodeEscrows(block Block, mint string, programs []string, money []Event) ([]EscrowEvent, error) {
	allowed := map[string]bool{}
	for _, p := range programs {
		allowed[p] = true
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	var txs []json.RawMessage
	if err := json.Unmarshal(block.Transactions, &txs); err != nil {
		return nil, err
	}
	result := []EscrowEvent{}
	seen := map[string]bool{}
	for txIndex, raw := range txs {
		var tx Transaction
		if json.Unmarshal(raw, &tx) != nil {
			return nil, fmt.Errorf("invalid escrow transaction")
		}
		keys, err := accountKeys(tx)
		if err != nil {
			return nil, err
		}
		relevant := false
		for _, key := range keys {
			if allowed[key] {
				relevant = true
			}
		}
		if !relevant {
			continue
		}
		if tx.Meta == nil || len(tx.Meta.Err) == 0 {
			return nil, fmt.Errorf("missing escrow transaction status")
		}
		if string(tx.Meta.Err) != "null" {
			continue
		}
		if len(tx.Transaction.Signatures) == 0 || !Valid58(tx.Transaction.Signatures[0], 64) {
			return nil, fmt.Errorf("invalid escrow signature")
		}
		signature := tx.Transaction.Signatures[0]
		if seen[signature] {
			continue
		}
		seen[signature] = true
		logs, err := ownedLogs(tx, keys, allowed)
		if err != nil {
			return nil, err
		}
		for outer, in := range tx.Transaction.Message.Instructions {
			program, err := programID(in, keys)
			if err != nil {
				return nil, err
			}
			if !allowed[program] {
				continue
			}
			parsed, err := parseEscrow(in, keys, mint)
			if err != nil {
				return nil, err
			}
			if parsed.skip {
				continue
			}
			candidates := []Event{}
			for _, m := range money {
				if m.Signature == signature && m.Outer == outer {
					candidates = append(candidates, m)
				}
			}
			if len(candidates) != 1 || candidates[0].Inner < 0 {
				return nil, fmt.Errorf("escrow action needs exactly one verified token CPI")
			}
			transfer := candidates[0]
			e := parsed.event
			if transfer.SenderAccount != parsed.source || transfer.RecipientAccount != parsed.destination {
				return nil, fmt.Errorf("escrow CPI account mismatch")
			}
			if e.Kind == "funding" {
				if transfer.Amount != e.Amount || transfer.Sender != e.Buyer || transfer.Recipient != e.Escrow {
					return nil, fmt.Errorf("deposit CPI terms mismatch")
				}
			} else {
				e.Amount = transfer.Amount
				if transfer.Sender != e.Escrow {
					return nil, fmt.Errorf("release/refund vault authority mismatch")
				}
				if e.Kind == "release" {
					e.Seller = transfer.Recipient
				} else {
					e.Buyer = transfer.Recipient
				}
			}
			emitted := logs[outer]
			if len(emitted) != 1 {
				return nil, fmt.Errorf("missing or ambiguous owned escrow event")
			}
			data := emitted[0]
			if e.Kind == "funding" {
				if len(data) != 120 || !bytes.Equal(data[:8], Discriminator("event", "Deposited")) || Encode58(data[8:40]) != e.Escrow || Encode58(data[40:72]) != e.Buyer || Encode58(data[72:104]) != e.Seller || strconv.FormatUint(binary.LittleEndian.Uint64(data[104:112]), 10) != e.Amount || int64(binary.LittleEndian.Uint64(data[112:120])) != e.RefundAt {
					return nil, fmt.Errorf("deposit event disagrees with instruction/transfer")
				}
			} else {
				status := byte(1)
				if e.Kind == "refund" {
					status = 2
				}
				if len(data) != 49 || !bytes.Equal(data[:8], Discriminator("event", "Finalized")) || Encode58(data[8:40]) != e.Escrow || data[40] != status || strconv.FormatUint(binary.LittleEndian.Uint64(data[41:49]), 10) != e.Amount {
					return nil, fmt.Errorf("terminal event disagrees with instruction/transfer")
				}
			}
			e.Signature = signature
			e.Outer = outer
			e.Inner = transfer.Inner
			e.LogIndex = (int64(outer) << 32) | int64(transfer.Inner+1)
			e.TransactionIndex = txIndex
			e.Raw = raw
			result = append(result, e)
		}
	}
	return result, nil
}
