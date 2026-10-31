package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/ktechbro/settlement-proof/internal/solana"
)

type EscrowEnvelope struct {
	Event      solana.EscrowEvent `json:"event"`
	Settlement Settlement         `json:"settlement"`
}

func (s *Store) insertEscrowEvents(ctx context.Context, tx pgx.Tx, events []solana.EscrowEvent) error {
	for _, e := range events {
		if e.Chain != s.Chain || e.EventKey != e.Key() {
			return fmt.Errorf("invalid escrow event namespace")
		}
		if err := tx.QueryRow(ctx, "SELECT id FROM settlements WHERE chain=$1 AND tx_hash=$2 AND log_index=$3", s.Chain, e.Signature, e.LogIndex).Scan(&e.SettlementID); err != nil {
			return err
		}
		body, _ := json.Marshal(e)
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO escrow_events(event_key,chain,program,escrow,signature,outer_instruction,settlement_id,fingerprint,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
   ON CONFLICT(event_key) DO UPDATE SET event_key=escrow_events.event_key WHERE escrow_events.fingerprint=EXCLUDED.fingerprint AND escrow_events.settlement_id=EXCLUDED.settlement_id RETURNING id`, e.EventKey, e.Chain, e.Program, e.Escrow, e.Signature, e.Outer, e.SettlementID, e.Fingerprint(), body).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: escrow event changed", ErrConflict)
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO escrow_event_scopes(scope,event_id) VALUES($1,$2) ON CONFLICT DO NOTHING", s.Scope, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EscrowVaults(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, "SELECT DISTINCT e.body->>'vault' FROM escrow_events e JOIN escrow_event_scopes x ON x.event_id=e.id WHERE x.scope=$1", s.Scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var vault string
		if err = rows.Scan(&vault); err != nil {
			return nil, err
		}
		out = append(out, vault)
	}
	return out, rows.Err()
}
func (s *Store) EscrowEvents(ctx context.Context, after int64, limit int) ([]EscrowEnvelope, error) {
	rows, err := s.Pool.Query(ctx, "SELECT e.id,e.body FROM escrow_events e JOIN escrow_event_scopes x ON x.event_id=e.id WHERE x.scope=$1 AND e.id>$2 ORDER BY e.id LIMIT $3", s.Scope, after, limit)
	if err != nil {
		return nil, err
	}
	events := []solana.EscrowEvent{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var event solana.EscrowEvent
		if err = json.Unmarshal(raw, &event); err != nil {
			rows.Close()
			return nil, err
		}
		event.ID = id
		events = append(events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []EscrowEnvelope{}
	for _, e := range events {
		values, err := s.List(ctx, Filter{After: e.SettlementID - 1, To: math.MaxInt64, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(values) != 1 || values[0].ID != e.SettlementID {
			return nil, fmt.Errorf("escrow settlement evidence missing")
		}
		out = append(out, EscrowEnvelope{Event: e, Settlement: values[0]})
	}
	return out, nil
}

func (s *Store) EscrowDetail(ctx context.Context, program, escrow string) (solana.EscrowProjection, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return solana.EscrowProjection{}, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT e.id,e.body FROM escrow_events e JOIN escrow_event_scopes x ON x.event_id=e.id WHERE x.scope=$1 AND e.program=$2 AND e.escrow=$3", s.Scope, program, escrow)
	if err != nil {
		return solana.EscrowProjection{}, err
	}
	events := []solana.EscrowEvent{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return solana.EscrowProjection{}, err
		}
		var e solana.EscrowEvent
		if err = json.Unmarshal(raw, &e); err != nil {
			rows.Close()
			return solana.EscrowProjection{}, err
		}
		e.ID = id
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return solana.EscrowProjection{}, err
	}
	if len(events) == 0 {
		return solana.EscrowProjection{}, pgx.ErrNoRows
	}
	projection := solana.ProjectEscrow(events)
	if err = tx.QueryRow(ctx, `SELECT (coalesce(sum(CASE WHEN s.recipient_account=$2 THEN s.amount ELSE 0 END),0)-coalesce(sum(CASE WHEN s.sender_account=$2 THEN s.amount ELSE 0 END),0))::text FROM settlements s JOIN settlement_scopes x ON x.settlement_id=s.id WHERE x.scope=$1 AND (s.recipient_account=$2 OR s.sender_account=$2)`, s.Scope, projection.Vault).Scan(&projection.ObservedVaultDelta); err != nil {
		return projection, err
	}
	if projection.Locked != "unknown" && projection.ObservedVaultDelta != projection.Locked {
		projection.Findings = append(projection.Findings, "unattributed_vault_movement")
		projection.State = "exception"
	}
	return projection, tx.Commit(ctx)
}
