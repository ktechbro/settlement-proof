package solana_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/ktechbro/settlement-proof/internal/solana"
	"github.com/ktechbro/settlement-proof/internal/testsolana"
)

func decode(t *testing.T, tx map[string]any) ([]solana.Event, error) {
	t.Helper()
	return (solana.Decoder{Mint: solana.USDCMint, Decimals: 6, Accounts: []string{testsolana.Source}}).Decode(testsolana.Block(101, 100, tx))
}
func TestDirectInnerVersionedAndExactAmounts(t *testing.T) {
	events, err := decode(t, testsolana.Transaction(1, []string{"50000000", "7000000"}, false))
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	if events[0].Outer != 0 || events[0].Inner != -1 || events[1].Outer != 1 || events[0].Sender != testsolana.Buyer || events[0].RecipientAccount != testsolana.Destination {
		t.Fatal(events)
	}
	events, err = decode(t, testsolana.Transaction(2, []string{"9000000", "1"}, true))
	if err != nil || len(events) != 2 || events[1].Outer != 0 || events[1].Inner != 1 {
		t.Fatal(events, err)
	}
	events, err = decode(t, testsolana.VersionedTransaction(3, math.MaxUint64))
	if err != nil || len(events) != 1 || events[0].Amount != "18446744073709551615" {
		t.Fatal(events, err)
	}
}
func TestFailedTransactionsAndDuplicateReceipts(t *testing.T) {
	tx := testsolana.Transaction(1, []string{"50000000"}, false)
	tx["meta"].(map[string]any)["err"] = map[string]any{"InstructionError": []any{0, "failure"}}
	events, err := decode(t, tx)
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	tx = testsolana.Transaction(2, []string{"7"}, false)
	events, err = (solana.Decoder{Mint: solana.USDCMint, Decimals: 6, Accounts: []string{testsolana.Source}}).Decode(testsolana.Block(101, 100, tx, tx))
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
}
func TestMissingOrUnsupportedEvidenceFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing balances", func(tx map[string]any) {
			m := tx["meta"].(map[string]any)
			m["preTokenBalances"] = []any{}
			m["postTokenBalances"] = []any{}
		}},
		{"missing CPI history", func(tx map[string]any) { tx["meta"].(map[string]any)["innerInstructions"] = nil }},
		{"failed caught CPI", func(tx map[string]any) {
			tx["meta"].(map[string]any)["logMessages"] = []string{"Program " + solana.TokenProgram + " failed: custom program error"}
		}},
		{"truncated logs", func(tx map[string]any) { tx["meta"].(map[string]any)["logMessages"] = []string{"Log truncated"} }},
		{"unknown version", func(tx map[string]any) { tx["version"] = 2 }},
		{"malformed quantity", func(tx map[string]any) {
			tx["transaction"].(map[string]any)["message"].(map[string]any)["instructions"].([]any)[0].(map[string]any)["parsed"].(map[string]any)["info"].(map[string]any)["amount"] = "18446744073709551616"
		}},
		{"unknown instruction", func(tx map[string]any) {
			tx["transaction"].(map[string]any)["message"].(map[string]any)["instructions"].([]any)[0].(map[string]any)["parsed"].(map[string]any)["type"] = "futureTransfer"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testsolana.Transaction(1, []string{"5"}, false)
			tt.edit(tx)
			if _, err := decode(t, tx); err == nil {
				t.Fatal("accepted incomplete evidence")
			}
		})
	}
}
func TestParsedLookupKeysAreNotExpandedTwice(t *testing.T) {
	tx := testsolana.Transaction(1, []string{"5"}, false)
	tx["version"] = 0
	message := tx["transaction"].(map[string]any)["message"].(map[string]any)
	message["addressTableLookups"] = []any{map[string]any{"accountKey": testsolana.Key(9)}}
	keys := message["accountKeys"].([]any)
	keys[2].(map[string]any)["source"] = "lookupTable"
	events, err := decode(t, tx)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
}

func TestV1ConfigurationIsRequiredAndPreserved(t *testing.T) {
	tx := testsolana.V1Transaction(7, []string{"5"})
	events, err := decode(t, tx)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	var preserved solana.Transaction
	if json.Unmarshal(events[0].Transaction, &preserved) != nil || len(preserved.Transaction.Message.Config) == 0 {
		t.Fatal("v1 configuration was discarded")
	}
	delete(tx["transaction"].(map[string]any)["message"].(map[string]any), "transactionConfig")
	if _, err := decode(t, tx); err == nil {
		t.Fatal("v1 message without configuration accepted")
	}
}
func TestHistoricalClosedDestinationAndMissingOwner(t *testing.T) {
	tx := testsolana.Transaction(1, []string{"5"}, false)
	meta := tx["meta"].(map[string]any)
	meta["postTokenBalances"] = []any{} // Both identities remain available before close.
	if events, err := decode(t, tx); err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	pre := meta["preTokenBalances"].([]any)
	pre[1].(map[string]any)["owner"] = ""
	if _, err := decode(t, tx); err == nil {
		t.Fatal("guessed a missing historical owner")
	}
}
func TestBase58CanonicalIdentity(t *testing.T) {
	for _, key := range []string{solana.TokenProgram, solana.USDCMint, testsolana.Source, "11111111111111111111111111111111"} {
		if !solana.Valid58(key, 32) {
			t.Fatal(key)
		}
	}
	if solana.Valid58("0OIl", 32) || solana.Valid58(testsolana.Signature(1), 32) {
		t.Fatal("invalid public key accepted")
	}
	var tx solana.Transaction
	raw, _ := json.Marshal(testsolana.VersionedTransaction(1, 5))
	if json.Unmarshal(raw, &tx) != nil {
		t.Fatal("invalid versioned fixture")
	}
}
