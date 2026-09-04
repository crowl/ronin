package terminal

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestEscapeDoesNotConsumeNextKey(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	term := &Terminal{input: r}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, _ = w.Write([]byte{27})
	key, err := term.ReadKey(ctx)
	if err != nil || key.Type != KeyEscape {
		t.Fatalf("escape: %v %v", key, err)
	}
	_, _ = w.Write([]byte("x\r\x1b[A\x1b[B"))
	for _, want := range []KeyType{KeyRune, KeyEnter, KeyArrowUp, KeyArrowDown} {
		key, err = term.ReadKey(ctx)
		if err != nil || key.Type != want {
			t.Fatalf("key=%v err=%v want=%v", key, err, want)
		}
	}
}

func TestSplitUTF8AndPasteTail(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	term := &Terminal{input: r}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() {
		_, _ = w.Write([]byte{0xc3})
		time.Sleep(time.Millisecond)
		_, _ = w.Write(append([]byte{0xa9}, []byte(PasteStart+"hello"+PasteEnd+"\r")...))
	}()
	key, err := term.ReadKey(ctx)
	if err != nil || key.Rune != 'é' {
		t.Fatalf("utf8=%v %v", key, err)
	}
	key, err = term.ReadKey(ctx)
	if err != nil || key.Type != KeyPaste || key.Text != "hello" {
		t.Fatalf("paste=%v %v", key, err)
	}
	key, err = term.ReadKey(ctx)
	if err != nil || key.Type != KeyEnter {
		t.Fatalf("tail=%v %v", key, err)
	}
}
