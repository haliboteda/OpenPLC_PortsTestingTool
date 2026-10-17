// Package pthold keeps a board's deadman (pt.hold) armed while the PC drives
// its outputs, so a PC that dies leaves nothing energised for longer than
// Span. The panel's timed run (ptpanel/hold.go) uses the same constants: see
// $PROD/docs/modules/M4/PROD-CONFIG-ITEMS.md "F · 安全联锁".
package pthold

import (
	"fmt"
	"sync"
	"time"

	"PortTool/src/ptboard"
)

// Two renewals in a row can be lost before the board drops its outputs; the
// renewal shares the UART with the sample frames, so Span must stay above the
// worst command queueing delay.
const (
	Span    = 6 * time.Second
	Renewal = 2 * time.Second
	Timeout = 800 * time.Millisecond
)

// Keeper renews one board's deadman until Release.
type Keeper struct {
	b    *ptboard.Board
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Arm arms the deadman now and renews it every Renewal. The error is the
// first arming's; renewals that fail are left to the board's own deadman.
func Arm(b *ptboard.Board) (*Keeper, error) {
	k := &Keeper{b: b, stop: make(chan struct{}), done: make(chan struct{})}
	if err := k.renew(); err != nil {
		close(k.done)
		return k, err
	}
	go func() {
		defer close(k.done)
		t := time.NewTicker(Renewal)
		defer t.Stop()
		for {
			select {
			case <-k.stop:
				return
			case <-t.C:
				_ = k.renew()
			}
		}
	}()
	return k, nil
}

// Command is the line that arms the deadman for one Span.
func Command() string { return fmt.Sprintf("pt.hold %d", Span.Milliseconds()) }

func (k *Keeper) renew() error {
	_, err := k.b.Send(Command(), ptboard.ExpectOne, Timeout)
	return err
}

// Release stops renewing and disarms. Call it after the sessions are stopped:
// disarming first would leave outputs driven with nothing watching the PC.
func (k *Keeper) Release() {
	k.once.Do(func() {
		close(k.stop)
		<-k.done
		_, _ = k.b.Send("pt.hold 0", ptboard.ExpectOne, Timeout)
	})
}
