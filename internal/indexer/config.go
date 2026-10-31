package indexer

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ktechbro/settlement-proof/internal/solana"
)

type Provider struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type Config struct {
	EscrowPrograms []string
	Protocol       string
	Decimals       int
	Chain          string
	ChainID        string
	Token          string
	Watch          []string
	Start          int64
	AnchorNumber   int64
	AnchorHash     string
	BatchSize      int64
	Replay         int64
	Providers      []Provider
	DatabaseURL    string
	Listen         string
	Poll           time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{Chain: os.Getenv("CHAIN"), ChainID: os.Getenv("CHAIN_ID"), Token: strings.ToLower(os.Getenv("TOKEN_ADDRESS")), AnchorHash: strings.ToLower(os.Getenv("ANCHOR_HASH")), DatabaseURL: os.Getenv("DATABASE_URL"), Listen: os.Getenv("LISTEN_ADDR"), Poll: 3 * time.Second, BatchSize: 100, Replay: 10}
	c.Protocol = os.Getenv("CHAIN_PROTOCOL")
	if c.Protocol == "" {
		c.Protocol = "evm"
	}
	if c.Protocol == "solana" {
		c.Token = os.Getenv("TOKEN_ADDRESS")
		c.AnchorHash = os.Getenv("ANCHOR_HASH")
		c.Replay = 0
		c.BatchSize = 5
		c.Decimals = 6
	}
	for _, p := range strings.Split(os.Getenv("ESCROW_PROGRAMS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.EscrowPrograms = append(c.EscrowPrograms, p)
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	for _, s := range strings.Split(os.Getenv("WATCH_ADDRESSES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			if c.Protocol != "solana" {
				s = strings.ToLower(s)
			}
			c.Watch = append(c.Watch, s)
		}
	}
	for _, item := range []struct {
		name     string
		target   *int64
		required bool
	}{{"START_BLOCK", &c.Start, true}, {"ANCHOR_NUMBER", &c.AnchorNumber, true}, {"BATCH_SIZE", &c.BatchSize, false}, {"REPLAY_BLOCKS", &c.Replay, false}} {
		value := os.Getenv(item.name)
		if value == "" {
			if item.required {
				return c, fmt.Errorf("%s is required", item.name)
			}
			continue
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return c, fmt.Errorf("invalid %s", item.name)
		}
		*item.target = n
	}
	if value := os.Getenv("POLL_INTERVAL"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			return c, fmt.Errorf("invalid POLL_INTERVAL")
		}
		c.Poll = d
	}
	if err := jsonProviders(os.Getenv("RPC_PROVIDERS"), &c.Providers); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Chain == "" || c.DatabaseURL == "" {
		return fmt.Errorf("CHAIN and DATABASE_URL are required")
	}
	if c.Protocol != "" && c.Protocol != "evm" && c.Protocol != "solana" {
		return fmt.Errorf("CHAIN_PROTOCOL must be evm or solana")
	}
	if c.Protocol == "solana" {
		if !solana.Valid58(c.ChainID, 32) || !solana.Valid58(c.Token, 32) || !solana.Valid58(c.AnchorHash, 32) {
			return fmt.Errorf("Solana genesis hash, mint and anchor must be canonical 32-byte base58 identifiers")
		}
		if c.AnchorNumber < 1 || c.AnchorNumber != c.Start-1 || c.Replay != 0 || c.BatchSize > 100 || c.Decimals != 6 {
			return fmt.Errorf("Solana requires START_BLOCK=ANCHOR_NUMBER+1, REPLAY_BLOCKS=0, BATCH_SIZE<=100 and six-decimal mint")
		}
	} else if _, err := Quantity(c.ChainID); err != nil {
		return fmt.Errorf("CHAIN_ID must be a hex quantity")
	}
	if c.Protocol != "solana" && (!validHex(c.Token, 20) || !validHex(c.AnchorHash, 32)) {
		return fmt.Errorf("invalid token address or anchor hash")
	}
	if c.Start < 1 || c.AnchorNumber < 0 || c.AnchorNumber >= c.Start || c.BatchSize < 1 || c.BatchSize > 10000 || c.Replay < 0 || c.Replay > 10000 || c.Poll < time.Millisecond {
		return fmt.Errorf("invalid scan bounds, anchor, batch size, replay, or polling interval")
	}
	if (len(c.Watch) == 0 && len(c.EscrowPrograms) == 0) || len(c.Providers) == 0 {
		return fmt.Errorf("at least one watch address and RPC provider required")
	}
	for _, p := range c.EscrowPrograms {
		if c.Protocol != "solana" || !solana.Valid58(p, 32) {
			return fmt.Errorf("escrow programs require Solana and valid program public keys")
		}
	}
	for _, a := range c.Watch {
		if (c.Protocol == "solana" && !solana.Valid58(a, 32)) || (c.Protocol != "solana" && !validHex(a, 20)) {
			return fmt.Errorf("invalid watch address")
		}
	}
	names := map[string]bool{}
	for _, p := range c.Providers {
		u, err := url.Parse(p.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || p.Name == "" || names[p.Name] {
			return fmt.Errorf("invalid provider configuration")
		}
		names[p.Name] = true
	}
	return nil
}

func (c Config) FinalityPolicy() string {
	if c.Protocol == "solana" {
		return "solana-finalized-blocks-v1"
	}
	return "rpc-finalized-v1"
}
