package ptitems

// Excludes says a and b cannot both be picked: one session in two modes at
// once (decision 102). A once-only item and a repeatable one of the same group
// run one after the other, never with both sessions open, so they do not
// exclude each other (decision 128, STG-03).
func Excludes(a, b Item) bool {
	return a.ID != b.ID && a.Port == b.Port && a.Exclusive != "" && a.Exclusive == b.Exclusive && a.Once == b.Once
}
