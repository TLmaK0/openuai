package config

import (
	"fmt"
	"reflect"
	"testing"
)

func TestAddRecentWorkDir(t *testing.T) {
	c := &Config{}
	for i := 0; i < 12; i++ {
		c.AddRecentWorkDir(fmt.Sprintf("/d%d", i))
	}
	c.AddRecentWorkDir("/d5")
	got := c.RecentWorkDirs()
	want := []string{"/d5", "/d11", "/d10", "/d9", "/d8", "/d7", "/d6", "/d4", "/d3", "/d2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent = %v", got)
	}
}
