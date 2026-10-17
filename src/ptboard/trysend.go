package ptboard

import "time"

// TrySend is Send for background polls that would rather skip a turn than
// queue: sent is false, and nothing is written, when a command is already in
// flight. Best effort - a command can still start between the check and the
// send, in which case this one waits its turn like any other.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6, "连接区".
func (b *Board) TrySend(cmd string, expect Expect, timeout time.Duration) (lines []string, sent bool, err error) {
	if !b.sendLock.TryLock() {
		return nil, false, nil
	}
	b.sendLock.Unlock()
	lines, err = b.Send(cmd, expect, timeout)
	return lines, true, err
}
