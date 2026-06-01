package shell

type Policy interface {
	Allow(args Args) error
}

type AllowAll struct{}

func (AllowAll) Allow(_ Args) error {
	return nil
}
