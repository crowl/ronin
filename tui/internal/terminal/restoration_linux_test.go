//go:build linux

package terminal

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStopRestoresRawModeAfterOutputFailure(t *testing.T) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer unix.Close(fd)
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	term, err := New(Config{Input: slave, Output: slave})
	if err != nil {
		t.Fatal(err)
	}
	broken, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	term.output = broken
	if err := term.Stop(); err == nil {
		t.Fatal("expected output error")
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *before != *after {
		t.Fatal("terminal mode not restored")
	}
	if err := term.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}
