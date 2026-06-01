package terminal

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTerminalReadKey(t *testing.T) {
	t.Run("parses split shift enter csi-u sequence", func(t *testing.T) {
		readFile, writeFile, err := os.Pipe()
		if err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer readFile.Close()
		defer writeFile.Close()

		term := &Terminal{input: readFile}

		go func() {
			_, _ = writeFile.Write([]byte{27})
			time.Sleep(time.Millisecond)
			_, _ = writeFile.Write([]byte("[13;2u"))
		}()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		key, err := term.ReadKey(ctx)
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
		if key.Type != KeyShiftEnter {
			t.Fatalf("key type\ngot:  %#v\nwant: %#v", key.Type, KeyShiftEnter)
		}
	})

	t.Run("parses slow split shift enter csi-u sequence", func(t *testing.T) {
		readFile, writeFile, err := os.Pipe()
		if err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer readFile.Close()
		defer writeFile.Close()

		term := &Terminal{input: readFile}

		go func() {
			_, _ = writeFile.Write([]byte{27})
			time.Sleep(25 * time.Millisecond)
			_, _ = writeFile.Write([]byte("[13;2u"))
		}()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		key, err := term.ReadKey(ctx)
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
		if key.Type != KeyShiftEnter {
			t.Fatalf("key type\ngot:  %#v\nwant: %#v", key.Type, KeyShiftEnter)
		}
	})

	t.Run("returns pending buffered key", func(t *testing.T) {
		term := &Terminal{
			pendingKeys: []Key{
				{Type: KeyRune, Rune: 'b'},
			},
		}

		key, err := term.ReadKey(context.Background())
		if err != nil {
			t.Fatalf("read buffered key: %v", err)
		}
		if key.Type != KeyRune || key.Rune != 'b' {
			t.Fatalf("buffered key\ngot:  %#v\nwant: rune b", key)
		}
	})

	t.Run("keeps plain escape", func(t *testing.T) {
		readFile, writeFile, err := os.Pipe()
		if err != nil {
			t.Fatalf("create pipe: %v", err)
		}
		defer readFile.Close()
		defer writeFile.Close()

		term := &Terminal{input: readFile}

		go func() {
			_, _ = writeFile.Write([]byte{27})
		}()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		key, err := term.ReadKey(ctx)
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
		if key.Type != KeyEscape {
			t.Fatalf("key type\ngot:  %#v\nwant: %#v", key.Type, KeyEscape)
		}
	})
}

func TestParseKeys(t *testing.T) {
	t.Run("parses paste payload", func(t *testing.T) {
		keys := ParseKeys([]byte(PasteStart + "hello\r\nworld" + PasteEnd))
		if len(keys) != 1 || keys[0].Type != KeyPaste || keys[0].Text != "hello\nworld" {
			t.Fatalf("paste keys\ngot:  %#v\nwant: []Key{{Type: KeyPaste, Text: %q}}", keys, "hello\nworld")
		}
	})

	t.Run("parses enhanced escape sequences", func(t *testing.T) {
		for _, data := range [][]byte{
			[]byte("\x1b[27u"),
			[]byte("\x1b[27;5u"),
			[]byte("\x1b[27;5;27~"),
		} {
			keys := ParseKeys(data)
			if len(keys) != 1 || keys[0].Type != KeyEscape {
				t.Fatalf("parse %q\ngot:  %#v\nwant: []Key{{Type: KeyEscape}}", string(data), keys)
			}
		}
	})

	t.Run("parses shift enter escape sequences", func(t *testing.T) {
		for _, data := range [][]byte{
			[]byte(KeyShiftEnterCSIu),
			[]byte(KeyShiftEnterModifyOther),
		} {
			keys := ParseKeys(data)
			if len(keys) != 1 || keys[0].Type != KeyShiftEnter {
				t.Fatalf("parse %q\ngot:  %#v\nwant: []Key{{Type: KeyShiftEnter}}", string(data), keys)
			}
		}
	})

	t.Run("does not treat unknown escape sequence as paste", func(t *testing.T) {
		keys := ParseKeys([]byte("\x1b[999u"))
		if len(keys) != 1 || keys[0].Type != KeyUnknown {
			t.Fatalf("keys\ngot:  %#v\nwant: []Key{{Type: KeyUnknown}}", keys)
		}
	})
}
