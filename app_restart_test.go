package main

import (
	"reflect"
	"testing"
)

func TestPendingStoreSurvivesRestartWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	s := loadPendingStore(dir)
	if !s.Add("wa:1", "n1") {
		t.Fatal("first add must succeed")
	}
	if s.Add("wa:1", "n1 again") {
		t.Fatal("duplicate key must be refused")
	}
	s.Add("wa:2", "n2")

	s2 := loadPendingStore(dir) // restart
	it, ok := s2.Peek()
	if !ok || it.Key != "wa:1" || it.Notification != "n1" {
		t.Fatalf("queue lost across restart: %+v %v", it, ok)
	}
	s2.Done("wa:1")

	s3 := loadPendingStore(dir) // restart again
	if s3.Len() != 1 {
		t.Fatalf("len=%d", s3.Len())
	}
	if s3.Add("wa:1", "redelivered") {
		t.Fatal("a message handled before the restart must not be processed again")
	}
}

func TestKilledProcsAreReportedOnce(t *testing.T) {
	dir := t.TempDir()
	writeProcsState(dir, []string{"p1 (npm run dev)"})
	if got := takeKilledProcs(dir); !reflect.DeepEqual(got, []string{"p1 (npm run dev)"}) {
		t.Fatalf("got %v", got)
	}
	if got := takeKilledProcs(dir); got != nil {
		t.Fatalf("reported twice: %v", got)
	}
	writeProcsState(dir, []string{"p1 (x)"})
	writeProcsState(dir, nil) // all exited
	if got := takeKilledProcs(dir); got != nil {
		t.Fatalf("exited processes reported: %v", got)
	}
}
