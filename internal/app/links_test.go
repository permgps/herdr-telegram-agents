package app

import "testing"

func TestTopicAndMessageLinks(t *testing.T) {
	if got := topicLink(-1001234567890, 42); got != "https://t.me/c/1234567890/42" {
		t.Errorf("topicLink = %s", got)
	}
	if got := topicLink(-1, 7); got != "https://t.me/c/1/7" {
		t.Errorf("topicLink small id = %s", got)
	}
	if got := messageLink(-1001234567890, 42, 1152); got != "https://t.me/c/1234567890/42/1152" {
		t.Errorf("messageLink = %s", got)
	}
}
