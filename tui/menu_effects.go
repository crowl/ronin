package tui

type noMenuEffect struct{}

type menuItemChange struct{ menuItem menuItem }

type menuItemPreSelected struct{ MenuItem menuItem }

type menuItemSelected struct{ MenuItem menuItem }

type hideMenu struct{}

type menuEffect interface{ menuEffect() }

func (noMenuEffect) menuEffect()        {}
func (menuItemChange) menuEffect()      {}
func (menuItemPreSelected) menuEffect() {}
func (menuItemSelected) menuEffect()    {}
func (hideMenu) menuEffect()            {}
