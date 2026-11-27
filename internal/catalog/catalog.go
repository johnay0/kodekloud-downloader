// Package catalog filters, lists and interactively selects courses.
package catalog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/johnay0/kodekloud-downloader/internal/api"
)

// PaidPlans are the plans --all-paid downloads.
var PaidPlans = []string{"Standard", "Pro"}

// Indexed is a course with its 1-based position in the full catalog.
type Indexed struct {
	No     int
	Course api.Course
}

func Index(cs []api.Course) []Indexed {
	out := make([]Indexed, len(cs))
	for i, c := range cs {
		out[i] = Indexed{i + 1, c}
	}
	return out
}

func Courses(items []Indexed) []api.Course {
	out := make([]api.Course, len(items))
	for i, it := range items {
		out[i] = it.Course
	}
	return out
}

// FilterByPlan keeps courses whose plan is in plans (case-insensitive).
func FilterByPlan(cs []api.Course, plans []string) []api.Course {
	want := map[string]bool{}
	for _, p := range plans {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			want[p] = true
		}
	}
	if len(want) == 0 {
		return cs
	}
	var out []api.Course
	for _, c := range cs {
		if want[strings.ToLower(strings.TrimSpace(c.Plan))] {
			out = append(out, c)
		}
	}
	return out
}

func names(ns []api.Named) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

// FilterByCategory keeps courses with a category containing any of the
// comma-separated tokens.
func FilterByCategory(items []Indexed, query string) []Indexed {
	var tokens []string
	for _, t := range strings.Split(query, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return items
	}
	var out []Indexed
	for _, it := range items {
	match:
		for _, cat := range it.Course.Categories {
			for _, tok := range tokens {
				if strings.Contains(strings.ToLower(cat.Name), tok) {
					out = append(out, it)
					break match
				}
			}
		}
	}
	return out
}

// Search keeps courses whose title, slug, instructors or categories contain q.
func Search(items []Indexed, q string) []Indexed {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []Indexed
	for _, it := range items {
		c := it.Course
		hay := strings.ToLower(strings.Join([]string{
			c.Title, c.Slug, strings.Join(names(c.Tutors), " "), strings.Join(names(c.Categories), " "),
		}, "\x00"))
		if strings.Contains(hay, q) {
			out = append(out, it)
		}
	}
	return out
}

// Table renders an ASCII table like prettytable.
func Table(header []string, rows [][]string, rightAlign map[int]bool) string {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	var b strings.Builder
	sep := "+"
	for _, w := range widths {
		sep += strings.Repeat("-", w+2) + "+"
	}
	line := func(cells []string) {
		b.WriteString("|")
		for i, c := range cells {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c))
			if rightAlign[i] {
				b.WriteString(" " + pad + c + " |")
			} else {
				b.WriteString(" " + c + pad + " |")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(sep + "\n")
	line(header)
	b.WriteString(sep + "\n")
	for _, r := range rows {
		line(r)
	}
	b.WriteString(sep)
	return b.String()
}

func CourseTable(items []Indexed) string {
	rows := make([][]string, len(items))
	for i, it := range items {
		inst := strings.Join(names(it.Course.Tutors), ", ")
		if inst == "" {
			inst = "N/A"
		}
		rows[i] = []string{strconv.Itoa(it.No), it.Course.Title, inst, it.Course.Plan, strings.Join(names(it.Course.Categories), ", ")}
	}
	return Table([]string{"No.", "Name", "Instructor", "Type", "Categories"}, rows, nil)
}

func CategoriesTable(cs []api.Course) string {
	counts := map[string]int{}
	for _, c := range cs {
		for _, cat := range c.Categories {
			counts[cat.Name]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	rows := make([][]string, len(keys))
	for i, k := range keys {
		rows[i] = []string{k, strconv.Itoa(counts[k])}
	}
	return Table([]string{"Category", "Courses"}, rows, map[int]bool{1: true})
}

var errNotNumbers = errors.New("not a number list")

// ParseInput parses "1,6-9,10-11" into numbers.
func ParseInput(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if a, b, ok := strings.Cut(part, "-"); ok {
			start, err1 := strconv.Atoi(strings.TrimSpace(a))
			end, err2 := strconv.Atoi(strings.TrimSpace(b))
			if err1 != nil || err2 != nil {
				return nil, errNotNumbers
			}
			if start > end {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			for i := start; i <= end; i++ {
				out = append(out, i)
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, errNotNumbers
		}
		out = append(out, n)
	}
	return out, nil
}

// Select shows the table and lets the user pick courses by number, search
// by keyword, filter with c:<category>, list categories (cats), reset (all)
// or quit (q). query/category pre-filter the first view.
func Select(all []api.Course, query, category string, in io.Reader, out io.Writer) []api.Course {
	full := Index(all)
	cur := full
	if category != "" {
		if f := FilterByCategory(cur, category); len(f) == 0 {
			fmt.Fprintf(out, "No courses found matching category filter '%s'. Showing all courses.\n", category)
		} else {
			cur = f
		}
	}
	if query != "" {
		if f := Search(cur, query); len(f) == 0 {
			fmt.Fprintf(out, "No courses found matching search filter '%s'. Showing previous view.\n", query)
		} else {
			cur = f
		}
	}
	fmt.Fprintln(out, CourseTable(cur))
	sc := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "Enter course number(s) to select (e.g. 1,6-9), search keyword, 'c:<cat>', or 'all'/'cats'/'q': ")
		if !sc.Scan() {
			fmt.Fprintln(out)
			return nil
		}
		input := strings.TrimSpace(sc.Text())
		lower := strings.ToLower(input)
		switch {
		case input == "":
			continue
		case lower == "q" || lower == "quit" || lower == "exit":
			return nil
		case lower == "all":
			cur = full
			fmt.Fprintln(out, CourseTable(cur))
			continue
		case lower == "cats" || lower == "categories":
			fmt.Fprintln(out, CategoriesTable(all))
			continue
		case strings.HasPrefix(lower, "c:") || strings.HasPrefix(lower, "cat:") || strings.HasPrefix(lower, "category:"):
			q := strings.TrimSpace(input[strings.Index(input, ":")+1:])
			if f := FilterByCategory(full, q); len(f) > 0 {
				cur = f
				fmt.Fprintln(out, CourseTable(cur))
				fmt.Fprintf(out, "Found %d course(s) in category matching '%s'.\n", len(f), q)
			} else {
				fmt.Fprintf(out, "No courses found in category matching '%s'. Type 'cats' to see all categories.\n", q)
			}
			continue
		}
		nums, err := ParseInput(input)
		if errors.Is(err, errNotNumbers) {
			if f := Search(full, input); len(f) > 0 {
				cur = f
				fmt.Fprintln(out, CourseTable(cur))
				fmt.Fprintf(out, "Found %d course(s) matching '%s'.\n", len(f), input)
			} else {
				fmt.Fprintf(out, "No courses found matching '%s'. Try another search.\n", input)
			}
			continue
		}
		if err != nil {
			fmt.Fprintf(out, "Error: %v\n", err)
			continue
		}
		var bad []int
		for _, n := range nums {
			if n < 1 || n > len(all) {
				bad = append(bad, n)
			}
		}
		if len(bad) > 0 {
			fmt.Fprintf(out, "Error: Course number(s) %v out of range (1 - %d).\n", bad, len(all))
			continue
		}
		sel := make([]api.Course, len(nums))
		for i, n := range nums {
			sel[i] = all[n-1]
		}
		return sel
	}
}
