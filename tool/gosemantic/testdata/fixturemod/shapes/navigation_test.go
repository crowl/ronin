package shapes

import "testing"

func TestCircleArea(t *testing.T) {
	circle := Circle{Radius: 2}
	if got := CircleArea(circle); got == 0 {
		t.Fatal("zero area")
	}
}
