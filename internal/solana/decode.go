// Package solana decodes legacy SPL Token transfers with historical account evidence.
package solana

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const TokenProgram = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
const USDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"

type Block struct {
	Hash         string          `json:"blockhash"`
	ParentHash   string          `json:"previousBlockhash"`
	ParentSlot   *int64          `json:"parentSlot"`
	Time         *int64          `json:"blockTime"`
	Transactions json.RawMessage `json:"transactions"`
}
type Instruction struct {
	ProgramID    string            `json:"programId"`
	ProgramIndex *int              `json:"programIdIndex"`
	Parsed       json.RawMessage   `json:"parsed"`
	Accounts     []json.RawMessage `json:"accounts"`
	Data         string            `json:"data"`
}
type Balance struct {
	Index   int    `json:"accountIndex"`
	Mint    string `json:"mint"`
	Owner   string `json:"owner"`
	Program string `json:"programId"`
	Token   struct {
		Amount   string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}
type Transaction struct {
	Transaction struct {
		Signatures []string `json:"signatures"`
		Message    struct {
			Config       json.RawMessage   `json:"transactionConfig"`
			Keys         []json.RawMessage `json:"accountKeys"`
			Instructions []Instruction     `json:"instructions"`
			Lookups      []json.RawMessage `json:"addressTableLookups"`
		} `json:"message"`
	} `json:"transaction"`
	Version json.RawMessage `json:"version"`
	Meta    *struct {
		Err   json.RawMessage `json:"err"`
		Inner *[]struct {
			Index        int           `json:"index"`
			Instructions []Instruction `json:"instructions"`
		} `json:"innerInstructions"`
		Logs   *[]string `json:"logMessages"`
		Pre    []Balance `json:"preTokenBalances"`
		Post   []Balance `json:"postTokenBalances"`
		Loaded *struct {
			Writable []string `json:"writable"`
			Readonly []string `json:"readonly"`
		} `json:"loadedAddresses"`
	} `json:"meta"`
}
type Event struct {
	Signature                       string
	Outer, Inner                    int
	Sender, Recipient               string
	SenderAccount, RecipientAccount string
	Amount                          string
	Transaction                     json.RawMessage
}
type Decoder struct {
	EscrowPrograms []string
	Mint           string
	Decimals       int
	Accounts       []string
}

func (d Decoder) Decode(block Block) ([]Event, error) {
	var rawTransactions []json.RawMessage
	if len(block.Transactions) == 0 || bytes.Equal(block.Transactions, []byte("null")) {
		return nil, fmt.Errorf("missing full transaction history")
	}
	if err := json.Unmarshal(block.Transactions, &rawTransactions); err != nil {
		return nil, fmt.Errorf("malformed transaction list")
	}
	events := []Event{}
	seen := map[string][]byte{}
	watch := map[string]bool{}
	for _, a := range d.Accounts {
		watch[a] = true
	}
	programs := map[string]bool{}
	for _, p := range d.EscrowPrograms {
		programs[p] = true
		watch[p] = true
	}
	for _, raw := range rawTransactions {
		var tx Transaction
		if err := json.Unmarshal(raw, &tx); err != nil {
			return nil, fmt.Errorf("malformed transaction")
		}
		keys, err := accountKeys(tx)
		if err != nil {
			return nil, err
		}
		relevant := false
		for _, key := range keys {
			if watch[key] {
				relevant = true
			}
		}
		if !relevant {
			continue
		}
		if tx.Meta == nil || len(tx.Meta.Err) == 0 {
			return nil, fmt.Errorf("missing transaction execution status")
		}
		if !bytes.Equal(tx.Meta.Err, []byte("null")) {
			continue
		} // Failed transactions have no token effects.
		for _, in := range tx.Transaction.Message.Instructions {
			program, err := programID(in, keys)
			if err != nil {
				return nil, err
			}
			if programs[program] {
				parsed, err := parseEscrow(in, keys, d.Mint)
				if err != nil {
					return nil, err
				}
				if !parsed.skip {
					watch[parsed.event.Vault] = true
				}
			}
		}
		if len(tx.Transaction.Signatures) == 0 || !Valid58(tx.Transaction.Signatures[0], 64) {
			return nil, fmt.Errorf("invalid transaction signature")
		}
		signature := tx.Transaction.Signatures[0]
		if prior, ok := seen[signature]; ok {
			if !bytes.Equal(prior, raw) {
				return nil, fmt.Errorf("conflicting duplicate transaction")
			}
			continue
		}
		seen[signature] = raw
		if len(tx.Version) > 0 && string(tx.Version) != `"legacy"` && string(tx.Version) != "0" && string(tx.Version) != "1" {
			return nil, fmt.Errorf("unsupported transaction version")
		}
		if string(tx.Version) == "1" {
			if len(tx.Transaction.Message.Config) == 0 || tx.Transaction.Message.Config[0] != '{' || len(tx.Transaction.Message.Lookups) > 0 {
				return nil, fmt.Errorf("invalid v1 message configuration or lookup tables")
			}
		}
		if tx.Meta.Inner == nil || tx.Meta.Logs == nil {
			return nil, fmt.Errorf("missing inner-instruction or execution-log history")
		}
		for _, log := range *tx.Meta.Logs {
			if strings.HasPrefix(log, "Program "+TokenProgram+" failed:") || strings.Contains(strings.ToLower(log), "log truncated") {
				return nil, fmt.Errorf("failed token CPI or truncated logs require investigation")
			}
		}
		balances := map[string]Balance{}
		for _, b := range append(append([]Balance(nil), tx.Meta.Pre...), tx.Meta.Post...) {
			if b.Index < 0 || b.Index >= len(keys) {
				return nil, fmt.Errorf("token balance account index out of bounds")
			}
			account := keys[b.Index]
			if old, ok := balances[account]; ok && (old.Mint != b.Mint || old.Owner != b.Owner || old.Program != b.Program) {
				return nil, fmt.Errorf("token identity/owner changed within transaction")
			}
			balances[account] = b
		}
		start := len(events)
		decode := func(instruction Instruction, outer, inner int) error {
			event, err := d.instruction(instruction, keys, balances, watch)
			if err != nil {
				return err
			}
			if event == nil {
				return nil
			}
			event.Signature = signature
			event.Outer = outer
			event.Inner = inner
			event.Transaction = append([]byte(nil), raw...)
			events = append(events, *event)
			return nil
		}
		for outer, instruction := range tx.Transaction.Message.Instructions {
			if err := decode(instruction, outer, -1); err != nil {
				return nil, err
			}
		}
		groups := map[int]bool{}
		for _, group := range *tx.Meta.Inner {
			if group.Index < 0 || group.Index >= len(tx.Transaction.Message.Instructions) || groups[group.Index] {
				return nil, fmt.Errorf("invalid or duplicate inner-instruction group")
			}
			groups[group.Index] = true
			for inner, instruction := range group.Instructions {
				if err := decode(instruction, group.Index, inner); err != nil {
					return nil, err
				}
			}
		}
		ordered := events[start:]
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Outer != ordered[j].Outer {
				return ordered[i].Outer < ordered[j].Outer
			}
			return ordered[i].Inner < ordered[j].Inner
		})
	}
	return events, nil
}

func accountKeys(tx Transaction) ([]string, error) {
	keys := []string{}
	objects := false
	stringsOnly := false
	for _, raw := range tx.Transaction.Message.Keys {
		var key string
		if json.Unmarshal(raw, &key) == nil {
			stringsOnly = true
		} else {
			var parsed struct {
				Key    string `json:"pubkey"`
				Source string `json:"source"`
			}
			if json.Unmarshal(raw, &parsed) != nil {
				return nil, fmt.Errorf("invalid account keys")
			}
			key = parsed.Key
			objects = true
		}
		if !Valid58(key, 32) {
			return nil, fmt.Errorf("invalid transaction account key")
		}
		keys = append(keys, key)
	}
	if objects && stringsOnly {
		return nil, fmt.Errorf("mixed account key encodings")
	}
	// jsonParsed already expands lookup-table keys. Raw JSON needs loaded-address expansion.
	if !objects && len(tx.Transaction.Message.Lookups) > 0 {
		if tx.Meta == nil || tx.Meta.Loaded == nil {
			return nil, fmt.Errorf("missing loaded addresses for versioned transaction")
		}
		for _, key := range append(append([]string(nil), tx.Meta.Loaded.Writable...), tx.Meta.Loaded.Readonly...) {
			if !Valid58(key, 32) {
				return nil, fmt.Errorf("invalid loaded address")
			}
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (d Decoder) instruction(in Instruction, keys []string, balances map[string]Balance, watch map[string]bool) (*Event, error) {
	program := in.ProgramID
	if in.ProgramIndex != nil {
		if *in.ProgramIndex < 0 || *in.ProgramIndex >= len(keys) {
			return nil, fmt.Errorf("program index out of bounds")
		}
		program = keys[*in.ProgramIndex]
	}
	if !Valid58(program, 32) {
		return nil, fmt.Errorf("missing or invalid instruction program identity")
	}
	if program != TokenProgram {
		return nil, nil
	}
	var source, dest, mint, amount string
	decimals := -1
	if len(in.Parsed) > 0 && string(in.Parsed) != "null" {
		var parsed struct {
			Type string `json:"type"`
			Info struct {
				Source      string `json:"source"`
				Destination string `json:"destination"`
				Mint        string `json:"mint"`
				Amount      string `json:"amount"`
				TokenAmount struct {
					Amount   string `json:"amount"`
					Decimals int    `json:"decimals"`
				} `json:"tokenAmount"`
			} `json:"info"`
		}
		if json.Unmarshal(in.Parsed, &parsed) != nil {
			return nil, fmt.Errorf("malformed parsed token instruction")
		}
		switch parsed.Type {
		case "transfer":
			source = parsed.Info.Source
			dest = parsed.Info.Destination
			amount = parsed.Info.Amount
		case "transferChecked":
			source = parsed.Info.Source
			dest = parsed.Info.Destination
			mint = parsed.Info.Mint
			amount = parsed.Info.TokenAmount.Amount
			decimals = parsed.Info.TokenAmount.Decimals
		case "initializeMint", "initializeMint2", "initializeAccount", "initializeAccount2", "initializeAccount3", "initializeMultisig", "initializeMultisig2", "approve", "approveChecked", "revoke", "setAuthority", "mintTo", "mintToChecked", "burn", "burnChecked", "closeAccount", "freezeAccount", "thawAccount", "syncNative", "getAccountDataSize", "initializeImmutableOwner", "amountToUiAmount", "uiAmountToAmount":
			return nil, nil
		default:
			return nil, fmt.Errorf("unsupported token instruction in watched transaction")
		}
	} else {
		accounts := []string{}
		for _, raw := range in.Accounts {
			var account string
			if json.Unmarshal(raw, &account) != nil {
				var n int
				if json.Unmarshal(raw, &n) != nil || n < 0 || n >= len(keys) {
					return nil, fmt.Errorf("instruction account index out of bounds")
				}
				account = keys[n]
			}
			accounts = append(accounts, account)
		}
		data, err := Decode58(in.Data)
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("missing compiled token instruction data")
		}
		switch data[0] {
		case 3:
			if len(data) != 9 || len(accounts) < 3 {
				return nil, fmt.Errorf("malformed compiled transfer")
			}
			source = accounts[0]
			dest = accounts[1]
			amount = strconv.FormatUint(binary.LittleEndian.Uint64(data[1:]), 10)
		case 12:
			if len(data) != 10 || len(accounts) < 4 {
				return nil, fmt.Errorf("malformed compiled checked transfer")
			}
			source = accounts[0]
			mint = accounts[1]
			dest = accounts[2]
			amount = strconv.FormatUint(binary.LittleEndian.Uint64(data[1:9]), 10)
			decimals = int(data[9])
		default:
			// Unparsed non-transfer operations are not safe to infer from an opcode alone.
			for _, a := range accounts {
				if watch[a] {
					return nil, fmt.Errorf("unsupported compiled token operation on watched account")
				}
			}
			return nil, nil
		}
	}
	if !watch[source] && !watch[dest] {
		return nil, nil
	}
	from, fromOK := balances[source]
	to, toOK := balances[dest]
	if !fromOK || !toOK {
		return nil, fmt.Errorf("missing historical source/destination token identity")
	}
	if from.Mint != d.Mint && to.Mint != d.Mint && mint != d.Mint {
		return nil, fmt.Errorf("watched account is not the configured mint")
	}
	if from.Mint != d.Mint || to.Mint != d.Mint || (mint != "" && mint != d.Mint) || from.Program != TokenProgram || to.Program != TokenProgram {
		return nil, fmt.Errorf("mint/program identity mismatch")
	}
	if !Valid58(from.Owner, 32) || !Valid58(to.Owner, 32) || from.Token.Decimals != d.Decimals || to.Token.Decimals != d.Decimals || (decimals >= 0 && decimals != d.Decimals) {
		return nil, fmt.Errorf("missing/invalid owner or token decimals")
	}
	n, err := strconv.ParseUint(amount, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != amount {
		return nil, fmt.Errorf("invalid atomic token quantity")
	}
	return &Event{Sender: from.Owner, Recipient: to.Owner, SenderAccount: source, RecipientAccount: dest, Amount: amount}, nil
}
