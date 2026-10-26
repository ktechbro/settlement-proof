package indexer

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Provider struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type Config struct {
	Chain        string
	ChainID      string
	Token        string
	Watch        []string
	Start        int64
	AnchorNumber int64
	AnchorHash   string
	BatchSize    int64
	Replay       int64
	Providers    []Provider
	DatabaseURL  string
	Listen       string
	Poll         time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{Chain: os.Getenv("CHAIN"), ChainID: os.Getenv("CHAIN_ID"), Token: strings.ToLower(os.Getenv("TOKEN_ADDRESS")), AnchorHash: strings.ToLower(os.Getenv("ANCHOR_HASH")), DatabaseURL: os.Getenv("DATABASE_URL"), Listen: os.Getenv("LISTEN_ADDR"), Poll: 3 * time.Second, BatchSize: 100, Replay: 10}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	for _, s := range strings.Split(os.Getenv("WATCH_ADDRESSES"), ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
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
	if _, err := Quantity(c.ChainID); err != nil {
		return fmt.Errorf("CHAIN_ID must be a hex quantity")
	}
	if !validHex(c.Token, 20) || !validHex(c.AnchorHash, 32) {
		return fmt.Errorf("invalid token address or anchor hash")
	}
	if c.Start < 1 || c.AnchorNumber < 0 || c.AnchorNumber >= c.Start || c.BatchSize < 1 || c.BatchSize > 10000 || c.Replay < 0 || c.Replay > 10000 || c.Poll < time.Millisecond {
		return fmt.Errorf("invalid scan bounds, anchor, batch size, replay, or polling interval")
	}
	if len(c.Watch) == 0 || len(c.Providers) == 0 {
		return fmt.Errorf("at least one watch address and RPC provider required")
	}
	for _, a := range c.Watch {
		if !validHex(a, 20) {
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
