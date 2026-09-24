package assertion

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

const APIVersion = "gametrace.assertion/v1"

type Case struct {
	APIVersion string      `yaml:"api_version"`
	Kind       string      `yaml:"kind"`
	Source     SourceSpec  `yaml:"source"`
	Assertions []Assertion `yaml:"assertions"`
}
type SourceSpec struct {
	Kind      string `yaml:"kind"`
	SessionID string `yaml:"session_id"`
}
type Assertion struct {
	Name       string             `yaml:"name"`
	Immediate  bool               `yaml:"immediate,omitempty"`
	Eventually *Window            `yaml:"eventually,omitempty"`
	Never      *Window            `yaml:"never,omitempty"`
	Trigger    Trigger            `yaml:"trigger"`
	Check      []string           `yaml:"check"`
	Capture    map[string]Capture `yaml:"capture,omitempty"`
}
type Window struct {
	Timeout time.Duration `yaml:"timeout"`
}
type Capture struct {
	From string `yaml:"from"`
}
type Trigger struct {
	Protocol  string `yaml:"protocol"`
	Direction string `yaml:"direction"`
	Semantic  string `yaml:"semantic"`
}

func (w *Window) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		Timeout string `yaml:"timeout"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	d, err := time.ParseDuration(raw.Timeout)
	if err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	w.Timeout = d
	return nil
}

func Load(data []byte) (*Case, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Case
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c.APIVersion != APIVersion {
		return nil, fmt.Errorf("api_version must be %q", APIVersion)
	}
	if c.Kind != "GameInterfaceCase" {
		return nil, fmt.Errorf("kind must be GameInterfaceCase")
	}
	if c.Source.Kind != "capture_session" || c.Source.SessionID == "" {
		return nil, fmt.Errorf("source must be capture_session with session_id")
	}
	seen := map[string]bool{}
	for i := range c.Assertions {
		// Omitting a lifecycle is the concise Immediate form documented for
		// single-event assertions.
		if !c.Assertions[i].Immediate && c.Assertions[i].Eventually == nil && c.Assertions[i].Never == nil {
			c.Assertions[i].Immediate = true
		}
		if err := c.Assertions[i].Validate(); err != nil {
			return nil, fmt.Errorf("assertions[%d]: %w", i, err)
		}
		if seen[c.Assertions[i].Name] {
			return nil, fmt.Errorf("duplicate assertion name %q", c.Assertions[i].Name)
		}
		seen[c.Assertions[i].Name] = true
	}
	return &c, nil
}

func (a Assertion) Validate() error {
	if a.Name == "" {
		return fmt.Errorf("name is required")
	}
	modes := 0
	if a.Immediate {
		modes++
	}
	if a.Eventually != nil {
		modes++
	}
	if a.Never != nil {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("exactly one of immediate, eventually, never is required")
	}
	if a.Eventually != nil && a.Eventually.Timeout <= 0 {
		return fmt.Errorf("eventually.timeout must be positive")
	}
	if a.Never != nil && a.Never.Timeout <= 0 {
		return fmt.Errorf("never.timeout must be positive")
	}
	if a.Trigger.Protocol == "" && a.Trigger.Direction == "" && a.Trigger.Semantic == "" {
		return fmt.Errorf("trigger is required")
	}
	if len(a.Check) == 0 {
		return fmt.Errorf("check is required")
	}
	if a.Never != nil && len(a.Capture) != 0 {
		return fmt.Errorf("never assertions cannot capture variables")
	}
	for name, cap := range a.Capture {
		if name == "" || cap.From == "" {
			return fmt.Errorf("capture entries require name and from")
		}
	}
	return nil
}
