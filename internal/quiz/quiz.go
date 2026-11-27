// Package quiz exports KodeKloud's public quizzes to Markdown.
package quiz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/johnay0/kodekloud-downloader/internal/naming"
)

var BaseURL = "https://mcq-backend-main.kodekloud.com/api"

// orderedValues decodes a JSON object's values in document order.
type orderedValues []string

func (o *orderedValues) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("questions: expected object")
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // key
			return err
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return err
		}
		*o = append(*o, fmt.Sprint(v))
	}
	return nil
}

type Quiz struct {
	Name      string        `json:"name"`
	Topic     string        `json:"topic"`
	Questions orderedValues `json:"questions"`
}

type Question struct {
	Question          string            `json:"question"`
	Answers           []string          `json:"answers"`
	CorrectAnswers    []string          `json:"correctAnswers"`
	Code              map[string]string `json:"code"`
	Explanation       string            `json:"explanation"`
	DocumentationLink string            `json:"documentationLink"`
}

func getJSON(ctx context.Context, c *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchQuestions fetches a quiz's questions concurrently, keeping order.
func fetchQuestions(ctx context.Context, c *http.Client, ids []string, workers int) ([]*Question, error) {
	qs := make([]*Question, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			var q *Question
			errs[i] = getJSON(ctx, c, BaseURL+"/questions/question?id="+url.QueryEscape(id), &q)
			qs[i] = q
		}()
	}
	wg.Wait()
	var out []*Question
	for i, q := range qs {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if q != nil && q.Question != "" {
			out = append(out, q)
		}
	}
	return out, nil
}

// Download writes KodeKloud_Quiz.md (or one file per quiz with sep) to dir.
func Download(ctx context.Context, c *http.Client, dir string, sep bool, workers int, log io.Writer) error {
	var quizzes []Quiz
	if err := getJSON(ctx, c, BaseURL+"/quizzes/all", &quizzes); err != nil {
		return err
	}
	fmt.Fprintf(log, "Total %d quiz available!\n", len(quizzes))
	var md []string
	if !sep {
		md = []string{"# KodeKloud Quiz"}
	}
	for i, qz := range quizzes {
		name := qz.Name
		if name == "" {
			name = qz.Topic
		}
		md = append(md, "\n## "+name)
		fmt.Fprintf(log, "Fetching Quiz %d - %s\n", i+1, name)
		questions, err := fetchQuestions(ctx, c, qz.Questions, workers)
		if err != nil {
			return err
		}
		for n, q := range questions {
			md = append(md, fmt.Sprintf("\n**%d. %s**", n+1, strings.TrimSpace(q.Question)), "\n")
			for _, a := range q.Answers {
				md = append(md, "* [ ] "+a)
			}
			md = append(md, "\n**Correct answer:**")
			for _, a := range q.CorrectAnswers {
				md = append(md, "* [x] "+a)
			}
			if s := q.Code["script"]; s != "" {
				md = append(md, "\n**Code**: \n"+s)
			}
			if q.Explanation != "" {
				md = append(md, "\n**Explaination**: "+q.Explanation)
			}
			if q.DocumentationLink != "" {
				md = append(md, "\n**Documentation Link**: "+q.DocumentationLink)
			}
		}
		if sep && name != "" {
			out := filepath.Join(dir, naming.Sanitize(name, 100)+".md")
			if err := os.WriteFile(out, []byte(strings.Join(md, "\n")), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(log, "Quiz file written in %s\n", out)
			md = nil
		} else {
			md = append(md, "\n---\n")
		}
	}
	if !sep {
		out := filepath.Join(dir, "KodeKloud_Quiz.md")
		if err := os.WriteFile(out, []byte(strings.Join(md, "\n")), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(log, "Quiz file written in %s\n", out)
	}
	return nil
}
