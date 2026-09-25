package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Recorder is an http.RoundTripper that keeps the last exchange it carried,
// so the lane can see the bytes an SDK method decoded.
type Recorder struct {
	Next   http.RoundTripper // nil means http.DefaultTransport
	Prefix string            // base path stripped from the recorded path ("/v1")

	mu   sync.Mutex
	last *exchange
}

type exchange struct {
	method, path string
	status       int // 0 when the request got no response
	body         []byte
}

func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	next := r.Next
	if next == nil {
		next = http.DefaultTransport
	}
	ex := &exchange{method: req.Method, path: strings.TrimPrefix(req.URL.EscapedPath(), r.Prefix)}
	resp, err := next.RoundTrip(req)
	if err == nil {
		ex.status = resp.StatusCode
		ex.body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(ex.body))
		if err != nil {
			ex.status, resp = 0, nil
		}
	}
	r.mu.Lock()
	r.last = ex
	r.mu.Unlock()
	return resp, err
}

func (r *Recorder) take() *exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	ex := r.last
	r.last = nil
	return ex
}

// Op is one row of the lane: an SDK method and the operation it must reach.
type Op struct {
	ID     string // operationId in the pinned spec
	Method string // the SDK method, "Client.Ticker"
	// Write puts the row in the write tier: it changes state, or needs state
	// the write tier creates. It runs only when writes are allowed.
	Write bool
	// Call invokes Method once. An iterator is read for one page.
	Call func(context.Context) error
}

// Skip is returned by a Call that cannot run, before it sends anything.
type Skip string

func (s Skip) Error() string { return string(s) }

// Config is what one run knows about its environment.
type Config struct {
	Spec     *Spec
	Recorder *Recorder
	// Credential is whether the client under test holds one. Without it no
	// private operation is called: each is skipped, or blocked when
	// RequireCredential is set, before any request is sent.
	Credential, RequireCredential bool
	AllowWrites                   bool
}

// Status is the outcome of one row.
type Status string

const (
	Pass       Status = "pass"       // called, and something in the answer was measured
	Unmeasured Status = "unmeasured" // called, and nothing in the answer to check
	Fail       Status = "fail"
	Skipped    Status = "skipped"
	Blocked    Status = "blocked" // a skip the run was told to treat as a failure
)

// Result is one row of the report.
type Result struct {
	Op        Op
	Operation Operation
	Status    Status
	Called    bool // the SDK method returned without error
	Items     int
	Category  string // why it failed: drift, route, http 502, decode, transport, schema
	Detail    string
	Findings  []Finding
}

// Run calls every op in order and judges each answer.
func Run(ctx context.Context, cfg Config, ops []Op) Report {
	rep := Report{Spec: cfg.Spec.Version}
	for _, op := range ops {
		rep.Results = append(rep.Results, run(ctx, cfg, op))
	}
	return rep
}

func run(ctx context.Context, cfg Config, op Op) Result {
	res := Result{Op: op}
	operation, ok := cfg.Spec.Ops[op.ID]
	if !ok {
		res.Status, res.Category = Fail, "drift"
		res.Detail = fmt.Sprintf("no operation %s in spec %s", op.ID, cfg.Spec.Version)
		return res
	}
	res.Operation = operation
	switch {
	case !operation.Public && !cfg.Credential && cfg.RequireCredential:
		res.Status, res.Category, res.Detail = Blocked, "credential", "no credential, and one is required; nothing sent"
		return res
	case !operation.Public && !cfg.Credential:
		res.Status, res.Detail = Skipped, "no credential; nothing sent"
		return res
	case op.Write && !cfg.AllowWrites:
		res.Status, res.Detail = Skipped, "write tier not enabled"
		return res
	}
	cfg.Recorder.take()
	err := op.Call(ctx)
	ex := cfg.Recorder.take()
	var skip Skip
	if errors.As(err, &skip) && ex == nil {
		res.Status, res.Detail = Skipped, skip.Error()
		return res
	}
	res.Status = Fail
	switch {
	case ex == nil:
		res.Category, res.Detail = "local", "no request sent"
		if err != nil {
			res.Detail = err.Error()
		}
		return res
	case !operation.routes(ex.method, ex.path):
		res.Category = "route"
		res.Detail = fmt.Sprintf("sent %s %s", ex.method, ex.path)
		return res
	case err != nil && ex.status >= 200 && ex.status < 300:
		res.Category, res.Detail = "decode", err.Error()
		return res
	case err != nil && ex.status == 0:
		res.Category, res.Detail = "transport", err.Error()
		return res
	case err != nil:
		res.Category, res.Detail = fmt.Sprintf("http %d", ex.status), err.Error()
		return res
	}
	res.Called = true
	if operation.Schema == nil {
		res.Status, res.Detail = Unmeasured, "[no response schema in the spec, nothing measured]"
		return res
	}
	res.Items, res.Findings = cfg.Spec.Check(operation, ex.body)
	switch {
	case len(res.Findings) > 0 && res.Findings[0].Severity == Error:
		res.Category = "schema"
	case res.Items == 0:
		res.Status, res.Detail = Unmeasured, "[0 items, nothing measured]"
	default:
		res.Status = Pass
		res.Detail = fmt.Sprintf("[%d item%s]", res.Items, map[bool]string{true: "s"}[res.Items != 1])
	}
	return res
}

// Report is the outcome of one run.
type Report struct {
	Spec    string
	Results []Result
	// Drift lists SDK methods the lane does not account for, and accounted
	// methods that no longer exist. Any entry fails the run.
	Drift []string
}

// Failed reports whether the run must fail: a failed or blocked row, or drift.
func (r Report) Failed() bool {
	for _, res := range r.Results {
		if res.Status == Fail || res.Status == Blocked {
			return true
		}
	}
	return len(r.Drift) > 0
}

// String is the report as printed: one row per operation with its findings,
// then the counts. called and measured are separate numbers on purpose.
func (r Report) String() string {
	var b strings.Builder
	counts := map[Status]int{}
	called, measured := 0, 0
	fmt.Fprintf(&b, "%-10s %-8s %-30s %-24s %-44s %-12s %s\n", "status", "surface", "method", "operation", "endpoint", "category", "detail")
	for _, res := range r.Results {
		counts[res.Status]++
		if res.Called {
			called++
			if res.Items > 0 {
				measured++
			}
		}
		endpoint := "-"
		if res.Operation.Path != "" {
			endpoint = res.Operation.Method + " " + res.Operation.Path
		}
		category := res.Category
		if category == "" {
			category = "-"
		}
		fmt.Fprintf(&b, "%-10s %-8s %-30s %-24s %-44s %-12s %s\n", res.Status, "go", res.Op.Method, res.Op.ID, endpoint, category, res.Detail)
		for _, f := range res.Findings {
			n := ""
			if f.N > 1 {
				n = fmt.Sprintf(" (x%d)", f.N)
			}
			path := f.Path
			if path == "" {
				path = "(body)"
			}
			fmt.Fprintf(&b, "%10s %-5s %s: %s%s\n", "", f.Severity, path, f.Msg, n)
		}
	}
	for _, d := range r.Drift {
		fmt.Fprintf(&b, "%-10s %s\n", "drift", d)
	}
	fmt.Fprintf(&b, "\nspec %s, %d operations: called %d, measured %d; failed %d, skipped %d, blocked %d, drift %d\n",
		r.Spec, len(r.Results), called, measured, counts[Fail], counts[Skipped], counts[Blocked], len(r.Drift))
	return b.String()
}
