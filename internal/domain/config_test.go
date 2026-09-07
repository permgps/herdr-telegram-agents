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

func TestConfigRoles(t *testing.T) {
	c := domain.Config{OperatorIDs: []int64{1, 2}, ObserverIDs: []int64{2, 3}}
	for id, want := range map[int64]domain.Role{1: domain.RoleOperator, 2: domain.RoleOperator, 3: domain.RoleObserver, 4: domain.RoleStranger, 0: domain.RoleStranger} {
		if got := c.Role(id); got != want {
			t.Errorf("Role(%d) = %s, want %s", id, got, want)
		}
	}
	if domain.RoleOperator.String() != "operator" || domain.RoleObserver.String() != "observer" || domain.RoleStranger.String() != "stranger" || domain.Role(9).String() != "stranger" {
		t.Error("Role.String")
	}
}

func TestConfigWithObserver(t *testing.T) {
	base := domain.Config{OperatorIDs: []int64{1}, ObserverIDs: []int64{3}}
	for name, id := range map[string]int64{"zero": 0, "negative": -5, "operator": 1, "duplicate": 3} {
		got, err := base.WithObserver(id)
		if !errors.Is(err, domain.ErrInvalidObserver) {
			t.Errorf("%s: WithObserver(%d) = %v, want ErrInvalidObserver", name, id, err)
		}
		if len(got.ObserverIDs) != 1 {
			t.Errorf("%s: returned config changed: %+v", name, got)
		}
	}
	next, err := base.WithObserver(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.ObserverIDs) != 2 || next.ObserverIDs[1] != 7 || !next.IsObserver(7) {
		t.Fatalf("WithObserver(7) = %+v", next)
	}
	if len(base.ObserverIDs) != 1 {
		t.Fatalf("receiver changed: %+v", base)
	}
	next.ObserverIDs[0] = 99
	if base.ObserverIDs[0] != 3 {
		t.Fatal("WithObserver shares the receiver's slice")
	}
}

func TestConfigWithoutObserver(t *testing.T) {
	base := domain.Config{OperatorIDs: []int64{1}, ObserverIDs: []int64{3, 7}}
	if _, err := base.WithoutObserver(8); !errors.Is(err, domain.ErrInvalidObserver) || !strings.Contains(err.Error(), "8 is not an observer") {
		t.Fatalf("WithoutObserver(8) = %v", err)
	}
	if _, err := base.WithoutObserver(1); !errors.Is(err, domain.ErrInvalidObserver) {
		t.Fatalf("WithoutObserver(operator) = %v", err)
	}
	next, err := base.WithoutObserver(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.ObserverIDs) != 1 || next.ObserverIDs[0] != 7 || len(base.ObserverIDs) != 2 {
		t.Fatalf("WithoutObserver(3) = %+v, base %+v", next, base)
	}
	last, err := next.WithoutObserver(7)
	if err != nil || last.ObserverIDs != nil {
		t.Fatalf("removing the last observer = %+v, %v", last, err)
	}
	if err := last.Validate(); err != nil && !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatal(err)
	}
}
