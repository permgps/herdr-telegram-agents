package app

import (
	"strconv"
	"strings"
)

// topicLink builds the t.me deep link for a topic in a supergroup: the
// chat id without its -100 prefix, then the thread id. Shared by the
// /status text, the dashboard and the pager.
func topicLink(chatID int64, threadID int) string {
	id := chatID
	if id < 0 {
		id = -id
	}
	s := strings.TrimPrefix(strconv.FormatInt(id, 10), "100")
	return "https://t.me/c/" + s + "/" + strconv.Itoa(threadID)
}

// messageLink builds the t.me deep link for one message inside a topic:
// the topic link with the message id appended. Telegram opens the topic
// scrolled to that message.
func messageLink(chatID int64, threadID, messageID int) string {
	return topicLink(chatID, threadID) + "/" + strconv.Itoa(messageID)
}
