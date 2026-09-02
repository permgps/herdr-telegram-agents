package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func validConfig() domain.Config {
	return domain.Config{
		Version:     domain.ConfigVersion,
		BotToken:    "1:abc",
		ChatID:      -1001,
		OperatorIDs: []int64{7},
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Config)
		want   string // substring of the error, "" for valid
	}{
		{"valid", func(*domain.Config) {}, ""},
		{"wrong version", func(c *domain.Config) { c.Version = 2 }, "version 2"},
		{"empty token", func(c *domain.Config) { c.BotToken = "" }, "bot_token"},
		{"positive chat id", func(c *domain.Config) { c.ChatID = 42 }, "chat_id"},
		{"zero chat id", func(c *domain.Config) { c.ChatID = 0 }, "chat_id"},
		{"no operators", func(c *domain.Config) { c.OperatorIDs = nil }, "operator_ids"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(&c)
			err := c.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !errors.Is(err, domain.ErrNotConfigured) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want ErrNotConfigured mentioning %q", err, tt.want)
			}
		})
	}
}

func TestConfigIsOperator(t *testing.T) {
	c := domain.Config{OperatorIDs: []int64{1, 2}}
	if !c.IsOperator(2) || c.IsOperator(3) {
		t.Fatalf("IsOperator: got 2=%v 3=%v", c.IsOperator(2), c.IsOperator(3))
	}
}
