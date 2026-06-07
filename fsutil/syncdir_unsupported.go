//go:build !(unix || darwin || linux)

package fsutil

func syncDir(_ string) error {
	return nil
}
