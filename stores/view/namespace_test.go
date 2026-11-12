package view

import "testing"

func TestPhysicalIDSeparatesNamesAndPreservesOrder(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"a", "a:b", "β", "view:"} {
		s := Store{Name: name}
		previous := ""
		for _, id := range []string{"a", "a:b", "b", "view:", "β"} {
			physical := PhysicalID(name, id)
			if seen[physical] || physical <= previous {
				t.Fatal("collision/order", name, id, physical)
			}
			seen[physical] = true
			previous = physical
			logical, ok := s.logicalID(physical)
			if !ok || logical != id {
				t.Fatal(logical, ok)
			}
			if _, ok := (&Store{Name: name + "x"}).logicalID(physical); ok {
				t.Fatal("foreign namespace accepted")
			}
		}
	}
}
