package solana_test

import (
	"testing"

	"github.com/ktechbro/settlement-proof/internal/solana"
	"github.com/ktechbro/settlement-proof/internal/testsolana"
)

func TestEscrowInstructionLogAndCPIAgreement(t *testing.T) {
	for _, kind := range []string{"funding", "release", "refund"} {
		t.Run(kind, func(t *testing.T) {
			block := testsolana.Block(103, 101, testsolana.EscrowTransaction(kind, 1, 11, 1000000, 1700000105))
			money, events, err := testsolana.DecodeEscrowFixture(block)
			if err != nil || len(events) != 1 || len(money) != 1 {
				t.Fatal(events, err)
			}
			e := events[0]
			if e.Kind != kind || e.Escrow != testsolana.EscrowAddress(1) || e.Vault != testsolana.VaultAddress(1) || e.Amount != "1000000" || e.Inner != 0 {
				t.Fatal(e)
			}
		})
	}
}
func TestEscrowRejectsSpoofedMissingOrMismatchedEvidence(t *testing.T) {
	for _, name := range []string{"spoofed log", "wrong CPI amount", "missing CPI", "wrong program attribution", "unknown instruction"} {
		t.Run(name, func(t *testing.T) {
			tx := testsolana.EscrowTransaction("funding", 1, 11, 1000000, 1700000105)
			meta := tx["meta"].(map[string]any)
			switch name {
			case "spoofed log":
				logs := meta["logMessages"].([]string)
				logs[4] = "Program log: " + logs[4]
			case "wrong CPI amount":
				meta["innerInstructions"].([]any)[0].(map[string]any)["instructions"].([]any)[0].(map[string]any)["parsed"].(map[string]any)["info"].(map[string]any)["tokenAmount"].(map[string]any)["amount"] = "1"
			case "missing CPI":
				meta["innerInstructions"] = []any{}
			case "wrong program attribution":
				logs := meta["logMessages"].([]string)
				logs[0] = "Program " + testsolana.Key(9) + " invoke [1]"
				logs[5] = "Program " + testsolana.Key(9) + " success"
			case "unknown instruction":
				tx["transaction"].(map[string]any)["message"].(map[string]any)["instructions"].([]any)[0].(map[string]any)["data"] = solana.Encode58([]byte("unknown!"))
			}
			if _, _, err := testsolana.DecodeEscrowFixture(testsolana.Block(101, 100, tx)); err == nil {
				t.Fatal("accepted unverified escrow action")
			}
		})
	}
}
func TestEscrowLifecycleReleasedRefundedAndLocked(t *testing.T) {
	source := testsolana.NewEscrow()
	all := []solana.EscrowEvent{}
	for _, slot := range []int64{101, 103, 108, 109} {
		_, events, err := testsolana.DecodeEscrowFixture(source.Blocks[slot])
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			e.Slot = slot
			e.BlockTime = *source.Blocks[slot].Time
			all = append(all, e)
		}
	}
	for deal, want := range map[byte]string{1: "released", 2: "refunded", 3: "funded"} {
		events := []solana.EscrowEvent{}
		for _, e := range all {
			if e.Escrow == testsolana.EscrowAddress(deal) {
				events = append(events, e)
			}
		}
		p := solana.ProjectEscrow(events)
		if p.State != want || len(p.Findings) != 0 {
			t.Fatal(p)
		}
		if deal != 3 && p.Locked != "0" {
			t.Fatal("terminal escrow retained principal", p)
		}
	}
	missing := solana.ProjectEscrow(all[1:2])
	if missing.State != "exception" {
		t.Fatal("missing funding history was inferred", missing)
	}
}
