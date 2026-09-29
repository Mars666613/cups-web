package main

import (
	"reflect"
	"testing"
)

func TestBuildEvenReversePages(t *testing.T) {
	tests := []struct {
		total int
		want  []int
	}{
		{total: 6, want: []int{6, 4, 2}},
		{total: 5, want: []int{0, 4, 2}},
		{total: 4, want: []int{4, 2}},
		{total: 3, want: []int{0, 2}},
		{total: 2, want: []int{2}},
		{total: 1, want: []int{0}},
		{total: 10, want: []int{10, 8, 6, 4, 2}},
		{total: 7, want: []int{0, 6, 4, 2}},
	}
	for _, tt := range tests {
		got := buildEvenReversePages(tt.total)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("buildEvenReversePages(%d) = %v, want %v", tt.total, got, tt.want)
		}
	}
}
