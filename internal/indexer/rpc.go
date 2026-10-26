package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

func jsonProviders(s string, out *[]Provider) error {
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("RPC_PROVIDERS must be a JSON array of name/url objects")
	}
	return nil
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("RPC error %d", e.Code) } // Provider messages may contain credentials.
func rangeError(err error) bool {
	var e *RPCError
	return errors.As(err, &e) && (e.Code == -32005 || e.Code == -32602)
}

type RPC struct {
	Provider Provider
	Client   *http.Client
	id       atomic.Int64
}

func NewRPC(p Provider) *RPC {
	return &RPC{Provider: p, Client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (r *RPC) Call(ctx context.Context, method string, params any, out any) error {
	id := r.id.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Provider.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid RPC request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("RPC transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("RPC HTTP status %d", resp.StatusCode)
	}
	const maxBody = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("RPC body read failure")
	}
	if len(data) > maxBody {
		return fmt.Errorf("RPC response too large")
	}
	var envelope struct {
		Version string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("malformed RPC response")
	}
	if envelope.ID != id || envelope.Version != "2.0" {
		return fmt.Errorf("RPC envelope identity mismatch")
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return fmt.Errorf("RPC result unavailable")
	}
	if err = json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("invalid RPC result shape")
	}
	return nil
}

func (r *RPC) Header(ctx context.Context, tag string) (Header, error) {
	var h Header
	if err := r.Call(ctx, "eth_getBlockByNumber", []any{tag, false}, &h); err != nil {
		return h, err
	}
	if _, err := Quantity(h.Number); err != nil {
		return h, err
	}
	if _, err := Quantity(h.Timestamp); err != nil {
		return h, err
	}
	if !validHex(h.Hash, 32) || !validHex(h.ParentHash, 32) {
		return h, fmt.Errorf("invalid header hashes")
	}
	return h, nil
}

func hexHeight(n int64) string { return fmt.Sprintf("0x%x", n) }
