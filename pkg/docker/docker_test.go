package docker

import (
	"reflect"
	"strings"
	"testing"
)

func TestNamesAndLabels(t *testing.T) {
	id := NewRunID("fake-smoke", 42)
	if !strings.HasPrefix(id, "fake-smoke-42-") || len(id) != len("fake-smoke-42-")+6 {
		t.Fatalf("run id %q", id)
	}
	if a, b := NewRunID("x", 1), NewRunID("x", 1); a == b {
		t.Fatal("run ids should differ")
	}
	if ContainerName("r", "n1") != "kuru-r-n1" {
		t.Fatal("container name")
	}
	if !reflect.DeepEqual(Labels("r", "n1"), map[string]string{"kuru": "1", "kuru.run": "r", "kuru.node": "n1"}) {
		t.Fatal("labels")
	}
	if _, ok := Labels("r", "")[LabelNode]; ok {
		t.Fatal("node label without node")
	}
}

func TestEnvListSorted(t *testing.T) {
	got := envList(map[string]string{"B": "2", "A": "1", "C": "x=y"})
	if !reflect.DeepEqual(got, []string{"A=1", "B=2", "C=x=y"}) {
		t.Fatal(got)
	}
}

func TestRemovedString(t *testing.T) {
	r := Removed{Containers: []string{"a"}}
	if r.Empty() || r.String() != "1 containers, 0 networks, 0 volumes" {
		t.Fatal(r.String())
	}
}
