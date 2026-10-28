package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) insertSettlements(ctx context.Context, tx pgx.Tx, provider string, values []Settlement) error {
	for _, v := range values {
		if v.Protocol == "" {
			v.Protocol = "evm"
		}
		if _, err := tx.Exec(ctx, "INSERT INTO observations(scope,block_hash,tx_hash,log_index,provider,raw) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", s.Scope, v.BlockHash, v.TransactionHash, v.LogIndex, provider, []byte(v.Raw)); err != nil {
			return err
		}
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO settlements(chain,asset,tx_hash,log_index,block_number,block_hash,block_time,sender,recipient,amount,provider,finalized_number,finalized_hash,raw,protocol,outer_instruction,inner_instruction,sender_account,recipient_account)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,$15,$16,$17,$18,$19)
   ON CONFLICT(chain,tx_hash,log_index) DO UPDATE SET tx_hash=settlements.tx_hash
   WHERE settlements.asset=EXCLUDED.asset AND settlements.block_hash=EXCLUDED.block_hash
    AND settlements.block_number=EXCLUDED.block_number AND settlements.block_time=EXCLUDED.block_time
    AND settlements.sender=EXCLUDED.sender AND settlements.recipient=EXCLUDED.recipient AND settlements.amount=EXCLUDED.amount
    AND settlements.protocol=EXCLUDED.protocol AND settlements.outer_instruction IS NOT DISTINCT FROM EXCLUDED.outer_instruction
    AND settlements.inner_instruction IS NOT DISTINCT FROM EXCLUDED.inner_instruction
    AND settlements.sender_account IS NOT DISTINCT FROM EXCLUDED.sender_account AND settlements.recipient_account IS NOT DISTINCT FROM EXCLUDED.recipient_account
   RETURNING id`, v.Chain, v.Asset, v.TransactionHash, v.LogIndex, v.BlockNumber, v.BlockHash, v.BlockTime, v.Sender, v.Recipient, v.Amount, v.Provider, v.FinalizedNumber, v.FinalizedHash, []byte(v.Raw), v.Protocol, v.OuterInstruction, v.InnerInstruction, nullable(v.SenderAccount), nullable(v.RecipientAccount)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: settlement identity changed", ErrConflict)
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO settlement_scopes(scope,settlement_id) VALUES($1,$2) ON CONFLICT DO NOTHING", s.Scope, id); err != nil {
			return err
		}
		v.ID = id
		payload, _ := json.Marshal(v)
		if _, err = tx.Exec(ctx, "INSERT INTO outbox(settlement_id,payload) VALUES($1,$2) ON CONFLICT DO NOTHING", id, payload); err != nil {
			return err
		}
	}
	return nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
