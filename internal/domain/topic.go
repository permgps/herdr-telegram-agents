package domain

// Topic is the plugin's view of one Telegram forum topic.
type Topic struct {
	ThreadID    int
	Name        string
	IconEmojiID string
	Closed      bool
}

// TopicPatch describes a partial edit of a topic. Nil fields are left as they
// are, so the adapter can batch a rename and an icon change into one call and
// skip the call entirely when nothing changed.
type TopicPatch struct {
	Name   *string
	Status *Status
}

// Empty reports whether the patch would change nothing.
func (p TopicPatch) Empty() bool {
	return p.Name == nil && p.Status == nil
}
