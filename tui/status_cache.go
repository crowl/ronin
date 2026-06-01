package tui

type statusBarCache struct {
	valid     bool
	cwd       string
	cwdStatus string
}

func (c *statusBarCache) CWDStatus(cwd string) string {
	if c.valid && c.cwd == cwd {
		return c.cwdStatus
	}

	c.cwd = cwd
	c.cwdStatus = statusBarCWDStatus(cwd)
	c.valid = true
	return c.cwdStatus
}

func (c *statusBarCache) Reset() {
	c.valid = false
	c.cwd = ""
	c.cwdStatus = ""
}
