package solana

import (
	"math/big"
	"sort"
)

type EscrowProjection struct {
	Program            string        `json:"program"`
	Escrow             string        `json:"escrow"`
	Config             string        `json:"config,omitempty"`
	DealID             string        `json:"deal_id,omitempty"`
	Buyer              string        `json:"buyer,omitempty"`
	Seller             string        `json:"seller,omitempty"`
	Mint               string        `json:"mint"`
	Vault              string        `json:"vault"`
	RefundAt           int64         `json:"refund_at,omitempty"`
	State              string        `json:"state"`
	Funded             string        `json:"funded_atomic"`
	Released           string        `json:"released_atomic"`
	Refunded           string        `json:"refunded_atomic"`
	Locked             string        `json:"locked_atomic"`
	ObservedVaultDelta string        `json:"observed_vault_delta_atomic,omitempty"`
	Findings           []string      `json:"findings"`
	Events             []EscrowEvent `json:"events"`
}

func ProjectEscrow(input []EscrowEvent) EscrowProjection {
	events := append([]EscrowEvent(nil), input...)
	sort.Slice(events, func(i, j int) bool {
		if events[i].Slot != events[j].Slot {
			return events[i].Slot < events[j].Slot
		}
		if events[i].TransactionIndex != events[j].TransactionIndex {
			return events[i].TransactionIndex < events[j].TransactionIndex
		}
		return events[i].Outer < events[j].Outer
	})
	p := EscrowProjection{State: "incomplete_evidence", Findings: []string{}, Events: events}
	funded, released, refunded := new(big.Int), new(big.Int), new(big.Int)
	var funding *EscrowEvent
	terminal := false
	for i := range events {
		e := events[i]
		n, ok := new(big.Int).SetString(e.Amount, 10)
		if !ok || n.Sign() <= 0 {
			p.Findings = append(p.Findings, "invalid_amount")
			continue
		}
		if p.Escrow == "" {
			p.Program = e.Program
			p.Escrow = e.Escrow
			p.Mint = e.Mint
			p.Vault = e.Vault
		}
		if e.Program != p.Program || e.Escrow != p.Escrow || e.Mint != p.Mint || e.Vault != p.Vault {
			p.Findings = append(p.Findings, "deal_identity_mismatch")
		}
		switch e.Kind {
		case "funding":
			funded.Add(funded, n)
			if funding != nil || terminal {
				p.Findings = append(p.Findings, "invalid_funding_transition")
			}
			if funding == nil {
				copy := e
				funding = &copy
				p.Config = e.Config
				p.DealID = e.DealID
				p.Buyer = e.Buyer
				p.Seller = e.Seller
				p.RefundAt = e.RefundAt
			}
			p.State = "funded"
			if e.BlockTime >= e.RefundAt {
				p.Findings = append(p.Findings, "invalid_funding_deadline")
			}
		case "release", "refund":
			if e.Kind == "release" {
				released.Add(released, n)
				p.State = "released"
			} else {
				refunded.Add(refunded, n)
				p.State = "refunded"
			}
			if terminal {
				p.Findings = append(p.Findings, "duplicate_terminal_action")
			}
			terminal = true
			if funding == nil {
				p.Findings = append(p.Findings, "missing_funding_history")
				continue
			}
			if e.Amount != funding.Amount {
				p.Findings = append(p.Findings, "principal_mismatch")
			}
			if e.Config != "" && e.Config != funding.Config {
				p.Findings = append(p.Findings, "config_mismatch")
			}
			if e.Kind == "release" {
				if e.Seller != funding.Seller {
					p.Findings = append(p.Findings, "seller_mismatch")
				}
				if e.BlockTime >= funding.RefundAt || e.ExpiresAt <= e.BlockTime || e.ExpiresAt > funding.RefundAt {
					p.Findings = append(p.Findings, "invalid_release_window")
				}
			} else {
				if e.Buyer != funding.Buyer {
					p.Findings = append(p.Findings, "buyer_mismatch")
				}
				if e.BlockTime < funding.RefundAt {
					p.Findings = append(p.Findings, "early_refund")
				}
			}
		default:
			p.Findings = append(p.Findings, "unsupported_action")
		}
	}
	locked := new(big.Int).Sub(new(big.Int).Set(funded), released)
	locked.Sub(locked, refunded)
	if funding != nil && locked.Sign() < 0 {
		p.Findings = append(p.Findings, "negative_locked_principal")
	}
	p.Funded = funded.String()
	p.Released = released.String()
	p.Refunded = refunded.String()
	p.Locked = locked.String()
	if funding == nil {
		p.Locked = "unknown"
	}
	if len(p.Findings) > 0 {
		p.State = "exception"
	}
	return p
}
