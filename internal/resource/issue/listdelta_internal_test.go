package issue

import "testing"

// TestAListThatCannotBeSplitExactlyStaysWhole covers the inputs no recording
// holds, because the sandbox and the rig both split cleanly. Each is a case
// where a split would be a guess, and the row keeps both lists, which is what
// it said before, rather than a delta that might be wrong.
func TestAListThatCannotBeSplitExactlyStaysWhole(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		item rawItem
	}{
		{"a sprint name holding the separator", rawItem{
			Field: "Sprint", FieldType: "custom",
			From: str("1"), FromString: str("Q3, the hard one"),
			To: str("1, 2"), ToString: str("Q3, the hard one, Q4"),
		}},
		{"a custom field called Sprint with no ids", rawItem{
			Field: "Sprint", FieldType: "custom",
			FromString: str("red, green"), ToString: str("red"),
		}},
		{"an id that is not a number", rawItem{
			Field: "Sprint", FieldType: "custom",
			From: str("1"), FromString: str("S 1"),
			To: str("1, x"), ToString: str("S 1, S x"),
		}},
		{"labels that only moved order", rawItem{
			Field: "labels", FieldType: "jira",
			FromString: str("a b"), ToString: str("b a"),
		}},
		{"a custom labels field, never measured", rawItem{
			Field: "labels", FieldType: "custom",
			FromString: str("a"), ToString: str("a b"),
		}},
		{"a field that holds one value", rawItem{
			Field: "status", FieldType: "jira",
			From: str("1"), FromString: str("To Do"), To: str("3"), ToString: str("Done"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := splitList(tc.item); ok {
				t.Errorf("split into %+v", got)
			}
		})
	}
}

// TestASplitCountsWhatItRemoves covers the arithmetic the recordings are too
// small to reach: a cleared list, a duplicate, and a sprint renamed between
// two saves, which is the same sprint and not a removal and an addition.
func TestASplitCountsWhatItRemoves(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		item rawItem
		want []listChange
	}{
		{"clearing every label", rawItem{
			Field: "labels", FieldType: "jira",
			FromString: str("a b"), ToString: str(""),
		}, []listChange{
			{element: element{name: "a"}, removed: true},
			{element: element{name: "b"}, removed: true},
		}},
		{"one of a duplicate", rawItem{
			Field: "labels", FieldType: "jira",
			FromString: str("a a"), ToString: str("a"),
		}, []listChange{{element: element{name: "a"}, removed: true}}},
		{"a first sprint on Data Center, whose empty side is null", rawItem{
			Field: "Sprint", FieldType: "custom", To: str("5"), ToString: str("S 5"),
		}, []listChange{{element: element{name: "S 5", id: "5"}}}},
		{"a sprint renamed while another was added", rawItem{
			Field: "Sprint", FieldType: "custom",
			From: str("1"), FromString: str("Old name"),
			To: str("1, 2"), ToString: str("New name, S 2"),
		}, []listChange{{element: element{name: "S 2", id: "2"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := splitList(tc.item)
			if !ok {
				t.Fatal("not split")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("change %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
