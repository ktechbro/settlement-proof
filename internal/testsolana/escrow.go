package testsolana

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/ktechbro/settlement-proof/internal/solana"
)

const EscrowProgram = "EPobTKNafzYrWNkQ4RXH2CH896ficZyPr4cigk9Hrpgm"

var EscrowConfig = Key(20)

func EscrowAddress(deal byte) string { return Key(40 + deal) }
func VaultAddress(deal byte) string  { return Key(60 + deal) }
func pubBytes(key string) []byte     { raw, _ := solana.Decode58(key); return raw }

// EscrowTransaction mirrors the checked-in Anchor SDK/account layouts and event bytes.
func EscrowTransaction(kind string, deal, sig byte, amount uint64, refundAt int64) map[string]any {
	escrow, vault := EscrowAddress(deal), VaultAddress(deal)
	var accounts []string
	var data, event []byte
	source, destination, fromOwner, toOwner := "", "", "", ""
	sourceIndex, destIndex := 0, 0
	switch kind {
	case "funding":
		accounts = []string{Buyer, EscrowConfig, escrow, vault, Source, solana.USDCMint, solana.TokenProgram, "11111111111111111111111111111111"}
		source = Source
		destination = vault
		fromOwner = Buyer
		toOwner = escrow
		sourceIndex = 4
		destIndex = 3
		data = make([]byte, 64)
		copy(data, solana.Discriminator("global", "deposit"))
		binary.LittleEndian.PutUint64(data[8:16], uint64(deal))
		binary.LittleEndian.PutUint64(data[16:24], amount)
		binary.LittleEndian.PutUint64(data[24:32], uint64(refundAt))
		copy(data[32:], pubBytes(Seller))
		event = make([]byte, 120)
		copy(event, solana.Discriminator("event", "Deposited"))
		copy(event[8:40], pubBytes(escrow))
		copy(event[40:72], pubBytes(Buyer))
		copy(event[72:104], pubBytes(Seller))
		binary.LittleEndian.PutUint64(event[104:112], amount)
		binary.LittleEndian.PutUint64(event[112:], uint64(refundAt))
	case "release":
		accounts = []string{EscrowConfig, escrow, vault, Destination, solana.USDCMint, solana.TokenProgram, "Sysvar1nstructions1111111111111111111111111"}
		source = vault
		destination = Destination
		fromOwner = escrow
		toOwner = Seller
		sourceIndex = 2
		destIndex = 3
		data = make([]byte, 16)
		copy(data, solana.Discriminator("global", "settle"))
		binary.LittleEndian.PutUint64(data[8:], uint64(refundAt-1))
	case "refund":
		accounts = []string{escrow, vault, Source, solana.USDCMint, solana.TokenProgram}
		source = vault
		destination = Source
		fromOwner = escrow
		toOwner = Buyer
		sourceIndex = 1
		destIndex = 2
		data = solana.Discriminator("global", "refund")
	}
	if kind != "funding" {
		event = make([]byte, 49)
		copy(event, solana.Discriminator("event", "Finalized"))
		copy(event[8:40], pubBytes(escrow))
		event[40] = 1
		if kind == "refund" {
			event[40] = 2
		}
		binary.LittleEndian.PutUint64(event[41:], amount)
	}
	transfer := map[string]any{"programId": solana.TokenProgram, "program": "spl-token", "stackHeight": 2, "parsed": map[string]any{"type": "transferChecked", "info": map[string]any{"source": source, "destination": destination, "mint": solana.USDCMint, "tokenAmount": map[string]any{"amount": fmt.Sprint(amount), "decimals": 6}}}}
	balances := []any{balance(sourceIndex, fromOwner), balance(destIndex, toOwner)}
	keys := append(append([]string(nil), accounts...), EscrowProgram)
	logs := []string{"Program " + EscrowProgram + " invoke [1]", "Program log: Instruction: " + kind, "Program " + solana.TokenProgram + " invoke [2]", "Program " + solana.TokenProgram + " success", "Program data: " + base64.StdEncoding.EncodeToString(event), "Program " + EscrowProgram + " success"}
	return map[string]any{"version": "legacy", "transaction": map[string]any{"signatures": []string{Signature(sig)}, "message": map[string]any{"accountKeys": keys, "instructions": []any{map[string]any{"programId": EscrowProgram, "accounts": accounts, "data": solana.Encode58(data)}}}}, "meta": map[string]any{"err": nil, "preTokenBalances": balances, "postTokenBalances": balances, "innerInstructions": []any{map[string]any{"index": 0, "instructions": []any{transfer}}}, "logMessages": logs}}
}

func NewEscrow() *Server {
	s := New()
	deadline := int64(1700000105)
	s.Blocks = map[int64]solana.Block{100: Block(100, 99), 101: Block(101, 100, EscrowTransaction("funding", 1, 31, 1000000, deadline)), 103: Block(103, 101, EscrowTransaction("release", 1, 32, 1000000, deadline), EscrowTransaction("funding", 2, 33, 2000000, deadline)), 108: Block(108, 103, EscrowTransaction("refund", 2, 34, 2000000, deadline)), 109: Block(109, 108, EscrowTransaction("funding", 3, 35, 3000000, deadline+100))}
	return s
}

func DecodeEscrowFixture(block solana.Block) ([]solana.Event, []solana.EscrowEvent, error) {
	money, err := (solana.Decoder{Mint: solana.USDCMint, Decimals: 6, EscrowPrograms: []string{EscrowProgram}}).Decode(block)
	if err != nil {
		return nil, nil, err
	}
	events, err := solana.DecodeEscrows(block, solana.USDCMint, []string{EscrowProgram}, money)
	return money, events, err
}

func UnsolicitedVaultTransfer(sig, deal byte, amount string) map[string]any {
	tx := Transaction(sig, []string{amount}, false)
	message := tx["transaction"].(map[string]any)["message"].(map[string]any)
	keys := message["accountKeys"].([]any)
	keys[2].(map[string]any)["pubkey"] = VaultAddress(deal)
	message["instructions"].([]any)[0].(map[string]any)["parsed"].(map[string]any)["info"].(map[string]any)["destination"] = VaultAddress(deal)
	meta := tx["meta"].(map[string]any)
	for _, label := range []string{"preTokenBalances", "postTokenBalances"} {
		raw, _ := json.Marshal(meta[label])
		var balances []map[string]any
		json.Unmarshal(raw, &balances)
		balances[1]["owner"] = EscrowAddress(deal)
		meta[label] = balances
	}
	return tx
}
