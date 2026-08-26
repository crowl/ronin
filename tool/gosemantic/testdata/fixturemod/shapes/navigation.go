package shapes

// Shape exposes area calculation.
type Shape interface {
	Area() float64
}

// Rectangle is another Shape implementation.
type Rectangle struct {
	Width, Height float64
}

// Area returns the rectangle's area.
func (r Rectangle) Area() float64 {
	return r.Width * r.Height
}

// CircleArea calls Circle.Area directly.
func CircleArea(c Circle) float64 {
	return c.Area()
}

// ShapeArea calls Area through the Shape interface.
func ShapeArea(s Shape) float64 {
	return s.Area()
}

// CombinedArea calls both a static and interface-dispatched helper.
func CombinedArea(c Circle, s Shape) float64 {
	return CircleArea(c) + ShapeArea(s)
}
