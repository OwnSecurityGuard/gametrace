package assertion

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gametrace/pkg/event"
	"github.com/expr-lang/expr"
	"github.com/tidwall/gjson"
)

type Status string

const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Invalid Status = "invalid"
)

// Result includes the evidence necessary to explain a decision without
// retaining a mutable copy of the source event.
type Result struct {
	Name                   string
	Status                 Status
	Reason                 string
	TriggerEventID         string
	MatchedEventIDs        []string
	WindowStart, WindowEnd time.Time
}

// Engine evaluates a case against a chronological Event Stream.
type Engine struct{}

func (Engine) Evaluate(c *Case, events []*event.Event, startedAt time.Time) ([]Result, error) {
	if c == nil {
		return nil, fmt.Errorf("nil case")
	}
	events = append([]*event.Event(nil), events...)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i] == nil {
			return false
		}
		if events[j] == nil {
			return true
		}
		if events[i].Identity.Timestamp.Equal(events[j].Identity.Timestamp) {
			return events[i].Identity.ID < events[j].Identity.ID
		}
		return events[i].Identity.Timestamp.Before(events[j].Identity.Timestamp)
	})
	vars := map[string]any{}
	results := make([]Result, 0, len(c.Assertions))
	for _, a := range c.Assertions {
		r := Result{Name: a.Name, Status: Fail, WindowStart: startedAt}
		var windowEnd time.Time
		if a.Eventually != nil {
			windowEnd = startedAt.Add(a.Eventually.Timeout)
		}
		if a.Never != nil {
			windowEnd = startedAt.Add(a.Never.Timeout)
		}
		r.WindowEnd = windowEnd
		var matched, forbidden bool
		for _, ev := range events {
			if ev == nil || ev.Identity.Timestamp.Before(startedAt) || (!windowEnd.IsZero() && ev.Identity.Timestamp.After(windowEnd)) {
				continue
			}
			ctx, err := NewEventContext(ev)
			if err != nil {
				return nil, err
			}
			if !a.matches(ctx) {
				continue
			}
			matched = true
			id := string(ev.Identity.ID)
			r.TriggerEventID = id
			r.MatchedEventIDs = append(r.MatchedEventIDs, id)
			ok, err := evaluateChecks(a.Check, ctx, vars)
			if err != nil {
				r.Status = Invalid
				r.Reason = err.Error()
				break
			}
			switch {
			case a.Never != nil && ok:
				r.Status = Fail
				r.Reason = "forbidden event matched"
				forbidden = true
			case a.Never != nil:
				continue
			case ok:
				if err := capture(a.Capture, ctx, vars); err != nil {
					r.Status = Invalid
					r.Reason = err.Error()
				} else {
					r.Status = Pass
					r.Reason = "checks passed"
				}
				matched = true
			case a.Immediate:
				r.Status = Fail
				r.Reason = "checks failed"
			default:
				continue
			}
			if a.Never == nil || r.Status != Pass {
				break
			}
		}
		if a.Never != nil && !forbidden {
			r.Status = Pass
			r.Reason = "no forbidden event in window"
		}
		if a.Eventually != nil && r.Status == Fail && r.Reason == "" {
			r.Reason = "no matching event passed before timeout"
		}
		if a.Immediate && !matched {
			r.Reason = "trigger event not found"
		}
		results = append(results, r)
	}
	return results, nil
}

func (a Assertion) matches(c EventContext) bool {
	return (a.Trigger.Protocol == "" || c.Meta["protocol"] == a.Trigger.Protocol) &&
		(a.Trigger.Direction == "" || c.Meta["direction"] == a.Trigger.Direction) &&
		(a.Trigger.Semantic == "" || c.Meta["semantic"] == a.Trigger.Semantic)
}

var variableRef = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
var existsPathRef = regexp.MustCompile(`exists\(\s*((?:meta|data|analysis)\.[A-Za-z0-9_.]+)\s*\)`)

func evaluateChecks(checks []string, c EventContext, vars map[string]any) (bool, error) {
	for _, raw := range checks {
		expression := variableRef.ReplaceAllString(raw, "vars.$1")
		expression = strings.ReplaceAll(expression, "contains(", "assertContains(")
		expression = strings.ReplaceAll(expression, "match(", "assertMatch(")
		// The YAML DSL accepts exists(data.roleId), while gjson needs the path
		// as a string. Rewriting the path (not its value) preserves a small,
		// serializable expression language without exposing reflection.
		expression = existsPathRef.ReplaceAllString(expression, `exists("$1")`)
		doc, err := json.Marshal(c)
		if err != nil {
			return false, err
		}
		env := map[string]any{"meta": c.Meta, "data": c.Data, "vars": vars}
		program, err := expr.Compile(expression, expr.Env(env), expr.AsBool(), expr.Function("exists", func(params ...any) (any, error) {
			if len(params) != 1 {
				return nil, fmt.Errorf("exists requires one path")
			}
			path, ok := params[0].(string)
			if !ok {
				return nil, fmt.Errorf("exists path must be string")
			}
			return gjson.GetBytes(doc, path).Exists(), nil
		}), expr.Function("assertContains", func(params ...any) (any, error) {
			if len(params) != 2 {
				return nil, fmt.Errorf("contains requires two arguments")
			}
			return strings.Contains(fmt.Sprint(params[0]), fmt.Sprint(params[1])), nil
		}), expr.Function("assertMatch", func(params ...any) (any, error) {
			if len(params) != 2 {
				return nil, fmt.Errorf("match requires two arguments")
			}
			return regexp.MatchString(fmt.Sprint(params[1]), fmt.Sprint(params[0]))
		}))
		if err != nil {
			return false, fmt.Errorf("compile %q: %w", raw, err)
		}
		out, err := expr.Run(program, env)
		if err != nil {
			return false, fmt.Errorf("run %q: %w", raw, err)
		}
		if !out.(bool) {
			return false, nil
		}
	}
	return true, nil
}

func capture(caps map[string]Capture, c EventContext, vars map[string]any) error {
	if len(caps) == 0 {
		return nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	for name, cap := range caps {
		value := gjson.GetBytes(b, cap.From)
		if !value.Exists() || value.Type == gjson.Null {
			return fmt.Errorf("capture %s: missing %s", name, cap.From)
		}
		v := value.Value()
		if old, exists := vars[name]; exists && fmt.Sprint(old) != fmt.Sprint(v) {
			return fmt.Errorf("capture %s: conflicting value", name)
		}
		vars[name] = v
	}
	return nil
}
