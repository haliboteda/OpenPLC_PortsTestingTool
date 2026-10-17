package testcase

// SD file transfer over the eth session in mode=file (PORTTOOL-FLOW.md B.11,
// DECISIONS.md 120): written, listed, read back, removed.
//
// Bench: the simulated board, whose eth session is bridged to a real TCP
// listener on 127.0.0.1 and whose card is an in-memory folder
// (src/simboard/c/stubs/entries_stub.c). It does not see a real SD card, FatFs, an
// exFAT volume or USB - the sim has no CDC port on this machine.

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"

	"PortTool/src/ptsdfile"
)

func dialSimEth() (io.ReadWriteCloser, error) {
	var err error
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
		var c net.Conn
		if c, err = net.DialTimeout("tcp", "127.0.0.1:5000", time.Second); err == nil {
			return c, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, err
}

func TestSDFileRoundTripOverEthOnTheSimulatedBoard(t *testing.T) {
	b := openSim(t)
	data := make([]byte, 3*ptsdfile.Chunk+123) // several acks, a short last block
	rand.New(rand.NewSource(7)).Read(data)

	acked := 0
	res, err := ptsdfile.Put(b, "eth", dialSimEth, "hello.bin", data, func(n int) { acked = n })
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !res.OK() || acked != len(data) {
		t.Fatalf("put %+v acked %d, want every byte written and the CRC read back", res, acked)
	}

	files, err := ptsdfile.List(b)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, f := range files {
		found = found || (f.Name == "hello.bin" && f.Size == int64(len(data)))
	}
	if !found {
		t.Fatalf("list %+v does not show hello.bin at %d bytes", files, len(data))
	}

	got, gres, err := ptsdfile.Get(b, "eth", dialSimEth, "hello.bin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !gres.OK() || !bytes.Equal(got, data) {
		t.Fatalf("get %+v: %d bytes back, equal=%v", gres, len(got), bytes.Equal(got, data))
	}

	if err := ptsdfile.Remove(b, "hello.bin"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if files, _ = ptsdfile.List(b); len(files) != 0 {
		t.Errorf("after remove the folder lists %+v", files)
	}

	var e *ptsdfile.Error
	if _, err := ptsdfile.Put(b, "eth", dialSimEth, "a/b", []byte("x"), nil); !errors.As(err, &e) || e.Code != "name" {
		t.Errorf("a name with a path character: err %v, want the board's error=name", err)
	}
	if err := ptsdfile.Eject(b); err != nil {
		t.Errorf("eject: %v", err)
	}
}
