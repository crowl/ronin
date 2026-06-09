// Package shapes provides simple geometric helpers for tests.
package shapes

// Pi is the ratio of a circle's circumference to its diameter.
const Pi = 3.14159

// DefaultName is used when a shape is unnamed.
var DefaultName = "shape"

// Circle is a round shape defined by its radius.
type Circle struct {
	Radius float64
}

// Area returns the area of the circle.
func (c Circle) Area() float64 {
	return Pi * c.Radius * c.Radius
}

// Scale grows the circle by factor.
func (c *Circle) Scale(factor float64) {
	c.Radius *= factor
}

// Describe returns a human description of any named value.
func Describe(name string) string {
	return name + " shape"
}

type hidden struct{}

func helper() {}
