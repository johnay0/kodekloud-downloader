package catalog

import (
	"bytes"
	"strings"
	"testing"

	"github.com/johnay0/kodekloud-downloader/internal/api"
)

func c(title, plan string, cats ...string) api.Course {
	var cs []api.Named
	for _, x := range cats {
		cs = append(cs, api.Named{Name: x})
	}
	return api.Course{Title: title, Slug: strings.ToLower(title), Plan: plan, Categories: cs, Tutors: []api.Named{{Name: "Mumshad"}}}
}

var all = []api.Course{
	c("Docker", "Free", "Containers"), c("CKA", "Standard", "Kubernetes", "Certification"),
	c("AWS Lambda", "Pro", "AWS", "Cloud"), c("Helm", "standard", "Kubernetes"),
}

func titles(cs []api.Course) string {
	var t []string
	for _, x := range cs {
		t = append(t, x.Title)
	}
	return strings.Join(t, ",")
}

func TestFilters(t *testing.T) {
	if got := titles(FilterByPlan(all, PaidPlans)); got != "CKA,AWS Lambda,Helm" {
		t.Errorf("paid = %s", got)
	}
	if got := titles(Courses(FilterByCategory(Index(all), "cloud, kube"))); got != "CKA,AWS Lambda,Helm" {
		t.Errorf("category = %s", got)
	}
	if got := titles(Courses(Search(Index(all), "mumshad"))); got != "Docker,CKA,AWS Lambda,Helm" {
		t.Errorf("search = %s", got)
	}
}

func TestParseInput(t *testing.T) {
	got, err := ParseInput("1-3,6,8-9")
	if err != nil || len(got) != 6 || got[5] != 9 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := ParseInput("5-2"); err == nil || !strings.Contains(err.Error(), "invalid range") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseInput("docker"); err != errNotNumbers {
		t.Errorf("err = %v", err)
	}
}

func TestSelectInteractive(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("kube\nc:aws\ncats\n9\n2,4\n")
	got := Select(all, "", "", in, &out)
	if titles(got) != "CKA,Helm" {
		t.Fatalf("selected %s\n%s", titles(got), out.String())
	}
	o := out.String()
	for _, want := range []string{"Found 2 course(s) matching 'kube'", "Found 1 course(s) in category matching 'aws'", "| Category", "out of range (1 - 4)"} {
		if !strings.Contains(o, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if Select(all, "", "", strings.NewReader("q\n"), &out) != nil {
		t.Error("q should select nothing")
	}
}

func TestTable(t *testing.T) {
	got := Table([]string{"A", "B"}, [][]string{{"1", "é"}}, map[int]bool{1: true})
	want := "+---+---+\n| A | B |\n+---+---+\n| 1 | é |\n+---+---+"
	if got != want {
		t.Errorf("table:\n%s", got)
	}
}
