package fixture

import (
	"sort"

	"gopkg.in/yaml.v3"
)

// This file makes fixtures compact and diffable (RFC §3.3): leaf objects emit in
// YAML flow style (one line, as in the RFC §5/§6 examples), and a fact that is
// merely `estimated` with no extra metadata collapses to a bare scalar — the
// shorthand a reader expands back to {value, confidence: estimated} (RFC §6.1).
//
// Compaction is lossless: a fact is only bared when it carries nothing but an
// estimated confidence, so parsing the bare scalar reconstructs the same fact.

// MarshalYAML emits a fact as a bare scalar when it is estimated with no via or
// error (the common fast-mode case), else as a one-line flow mapping.
func (f Fact[T]) MarshalYAML() (any, error) {
	if f.Confidence == Estimated && f.Via == "" && f.Error == 0 {
		return f.Value, nil
	}
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	if err := appendField(n, "value", f.Value); err != nil {
		return nil, err
	}
	if f.Confidence != "" {
		appendScalar(n, "confidence", string(f.Confidence))
	}
	if f.Via != "" {
		appendScalar(n, "via", f.Via)
	}
	if f.Error != 0 {
		if err := appendField(n, "error", f.Error); err != nil {
			return nil, err
		}
	}
	return n, nil
}

// MarshalYAML emits length stats as a one-line flow mapping of present fields.
func (l Length) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	if err := appendOptInt(n, "min", l.Min); err != nil {
		return nil, err
	}
	if err := appendOptInt(n, "max", l.Max); err != nil {
		return nil, err
	}
	if err := appendOptFloat(n, "mean", l.Mean); err != nil {
		return nil, err
	}
	if err := appendOptInt(n, "p95", l.P95); err != nil {
		return nil, err
	}
	return n, nil
}

// MarshalYAML emits a numeric/temporal range as a one-line flow mapping.
func (r Range) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	if r.Min != nil {
		if err := appendField(n, "min", r.Min); err != nil {
			return nil, err
		}
	}
	if r.Max != nil {
		if err := appendField(n, "max", r.Max); err != nil {
			return nil, err
		}
	}
	if err := appendOptFloat(n, "mean", r.Mean); err != nil {
		return nil, err
	}
	// The confidence of the EXTREMES. Dropped here it is silently lost, and a
	// consumer cannot tell a sampled range from an exact one — which is the whole
	// difference between a finding that can be trusted and one that cannot fire.
	if r.Confidence != "" {
		appendScalar(n, "confidence", string(r.Confidence))
	}
	return n, nil
}

// MarshalYAML emits the engine as a one-line flow mapping (RFC §5).
func (e Engine) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	appendScalar(n, "name", e.Name)
	appendScalar(n, "version", e.Version)
	return n, nil
}

// MarshalYAML emits a fan-out summary as a one-line flow mapping (RFC §6.6).
func (fo Fanout) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	if err := appendField(n, "mean", fo.Mean); err != nil {
		return nil, err
	}
	if fo.P50 != 0 {
		if err := appendField(n, "p50", fo.P50); err != nil {
			return nil, err
		}
	}
	if fo.P95 != 0 {
		if err := appendField(n, "p95", fo.P95); err != nil {
			return nil, err
		}
	}
	if fo.Max != 0 {
		if err := appendField(n, "max", fo.Max); err != nil {
			return nil, err
		}
	}
	if fo.Confidence != "" {
		appendScalar(n, "confidence", string(fo.Confidence))
	}
	return n, nil
}

// MarshalYAML emits a table: rows/bytes/columns in block style (columns as a
// map, one compact column per line), and the constraint/index/reference lists as
// flow sequences so a table's structure stays terse (RFC §3.3).
func (t Table) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	if err := appendField(n, "rows", t.Rows); err != nil {
		return nil, err
	}
	if t.Bytes != 0 {
		if err := appendField(n, "bytes", t.Bytes); err != nil {
			return nil, err
		}
	}
	if len(t.Columns) > 0 {
		if err := appendField(n, "columns", t.Columns); err != nil {
			return nil, err
		}
	}
	if err := appendFlowSeq(n, "constraints", len(t.Constraints), t.Constraints); err != nil {
		return nil, err
	}
	if err := appendFlowSeq(n, "indexes", len(t.Indexes), t.Indexes); err != nil {
		return nil, err
	}
	if err := appendFlowSeq(n, "references", len(t.References), t.References); err != nil {
		return nil, err
	}
	if t.Partitions != nil {
		pn := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		if err := appendField(pn, "count", t.Partitions.Count); err != nil {
			return nil, err
		}
		appendScalar(pn, "strategy", t.Partitions.Strategy)
		// The key is what lets a partitioned table be REBUILT rather than merely
		// described; dropped here it is silently lost, whatever the struct tag says.
		if t.Partitions.Key != "" {
			appendScalar(pn, "key", t.Partitions.Key)
		}
		if t.Partitions.Skew != 0 {
			if err := appendField(pn, "skew", t.Partitions.Skew); err != nil {
				return nil, err
			}
		}
		n.Content = append(n.Content, keyNode("partitions"), pn)
	}
	if err := appendSortedX(n, t.X); err != nil {
		return nil, err
	}
	return n, nil
}

// appendFlowSeq appends a flow-style sequence (its items still render as their
// own one-line flow mappings). It is a no-op for an empty list.
func appendFlowSeq(n *yaml.Node, key string, length int, seq any) error {
	if length == 0 {
		return nil
	}
	var sn yaml.Node
	if err := sn.Encode(seq); err != nil {
		return err
	}
	sn.Style = yaml.FlowStyle
	n.Content = append(n.Content, keyNode(key), &sn)
	return nil
}

// MarshalYAML emits a column as a one-line flow mapping. Columns are the bulk of
// a fixture, so keeping each to a single line is what keeps a 200-table schema
// committable (RFC §3.3, §4 "flat where possible") while still diffing cleanly at
// column granularity.
func (c Column) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	appendScalar(n, "type", c.Type)
	if err := appendField(n, "nullable", c.Nullable); err != nil {
		return nil, err
	}
	add := func(key string, val any) error { return appendField(n, key, val) }
	if c.NullFraction != nil {
		if err := add("null_fraction", *c.NullFraction); err != nil {
			return nil, err
		}
	}
	if c.Distinct != nil {
		if err := add("distinct", *c.Distinct); err != nil {
			return nil, err
		}
	}
	if c.Unique != nil {
		if err := add("unique", *c.Unique); err != nil {
			return nil, err
		}
	}
	if c.Generated != "" {
		appendScalar(n, "generated", c.Generated)
	}
	// Both must be emitted or the target rebuilds a generated column as an ordinary
	// one and accepts writes production rejects — this marshaller names every field
	// explicitly, so a field absent here is silently dropped.
	if c.Identity != "" {
		appendScalar(n, "identity", c.Identity)
	}
	if c.GeneratedExpression != "" {
		appendScalar(n, "generated_expression", c.GeneratedExpression)
	}
	if c.Default != "" {
		appendScalar(n, "default", c.Default)
	}
	if c.Format != "" {
		appendScalar(n, "format", c.Format)
	}
	if c.Length != nil {
		if err := add("length", *c.Length); err != nil {
			return nil, err
		}
	}
	if len(c.Values) > 0 {
		if err := add("values", c.Values); err != nil {
			return nil, err
		}
	}
	if len(c.Frequencies) > 0 {
		if err := add("frequencies", c.Frequencies); err != nil {
			return nil, err
		}
	}
	if c.Range != nil {
		if err := add("range", *c.Range); err != nil {
			return nil, err
		}
	}
	if c.Histogram != nil {
		if err := add("histogram", *c.Histogram); err != nil {
			return nil, err
		}
	}
	if c.Shape != nil {
		if err := add("shape", c.Shape); err != nil {
			return nil, err
		}
	}
	if len(c.Redact) > 0 {
		rn, err := c.Redact.MarshalYAML()
		if err != nil {
			return nil, err
		}
		var vn yaml.Node
		if err := vn.Encode(rn); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, keyNode("redact"), &vn)
	}
	if err := appendSortedX(n, c.X); err != nil {
		return nil, err
	}
	return n, nil
}

// MarshalYAML emits a constraint as a one-line flow mapping (RFC §6.4).
func (c Constraint) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	appendScalar(n, "name", c.Name)
	appendScalar(n, "kind", c.Kind)
	if len(c.Columns) > 0 {
		if err := appendField(n, "columns", c.Columns); err != nil {
			return nil, err
		}
	}
	if c.NullsDistinct != nil {
		if err := appendField(n, "nulls_distinct", *c.NullsDistinct); err != nil {
			return nil, err
		}
	}
	if c.Expression != "" {
		appendScalar(n, "expression", c.Expression)
	}
	if c.Validated != nil {
		if err := appendField(n, "validated", *c.Validated); err != nil {
			return nil, err
		}
	}
	if err := appendSortedX(n, c.X); err != nil {
		return nil, err
	}
	return n, nil
}

// MarshalYAML emits an index as a one-line flow mapping (RFC §6.5).
func (i Index) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	appendScalar(n, "name", i.Name)
	appendScalar(n, "method", i.Method)
	if len(i.Columns) > 0 {
		if err := appendField(n, "columns", i.Columns); err != nil {
			return nil, err
		}
	}
	// Keys must be emitted or an expression index survives the catalog read only to
	// be lost on the way to disk — this marshaller names every field explicitly, so a
	// field absent here is silently dropped no matter what the struct tag says.
	if len(i.Keys) > 0 {
		if err := appendField(n, "keys", i.Keys); err != nil {
			return nil, err
		}
	}
	// Include is payload, not key — and it has to survive for the same reason Keys
	// does: dropped here, a covering UNIQUE index reaches the disposable database
	// with its payload folded into the key, enforcing strictly less than production.
	if len(i.Include) > 0 {
		if err := appendField(n, "include", i.Include); err != nil {
			return nil, err
		}
	}
	if i.Unique {
		if err := appendField(n, "unique", i.Unique); err != nil {
			return nil, err
		}
	}
	if i.Partial != "" {
		appendScalar(n, "partial", i.Partial)
	}
	if i.Bytes != 0 {
		if err := appendField(n, "bytes", i.Bytes); err != nil {
			return nil, err
		}
	}
	if i.BloatEstimate != nil {
		if err := appendField(n, "bloat_estimate", *i.BloatEstimate); err != nil {
			return nil, err
		}
	}
	if err := appendSortedX(n, i.X); err != nil {
		return nil, err
	}
	return n, nil
}

// MarshalYAML emits a reference as a one-line flow mapping (RFC §6.6).
func (r Reference) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	appendScalar(n, "column", r.Column)
	appendScalar(n, "to", r.To)
	// Name groups the per-column entries of a composite foreign key back into one
	// constraint. Dropped here, a consumer rebuilds two independent single-column
	// keys instead — this marshaller names every field explicitly, so a field absent
	// here is silently lost no matter what the struct tag says.
	if r.Name != "" {
		appendScalar(n, "name", r.Name)
	}
	if r.OnDelete != "" {
		appendScalar(n, "on_delete", r.OnDelete)
	}
	if r.OnUpdate != "" {
		appendScalar(n, "on_update", r.OnUpdate)
	}
	if r.Validated != nil {
		if err := appendField(n, "validated", *r.Validated); err != nil {
			return nil, err
		}
	}
	if r.Fanout != nil {
		fn, err := r.Fanout.MarshalYAML()
		if err != nil {
			return nil, err
		}
		n.Content = append(n.Content, keyNode("fanout"), fn.(*yaml.Node))
	}
	if r.OrphanFraction != nil {
		if err := appendField(n, "orphan_fraction", *r.OrphanFraction); err != nil {
			return nil, err
		}
	}
	if err := appendSortedX(n, r.X); err != nil {
		return nil, err
	}
	return n, nil
}

// appendField encodes an arbitrary value and appends key: value to a mapping.
func appendField(n *yaml.Node, key string, val any) error {
	var vn yaml.Node
	if err := vn.Encode(val); err != nil {
		return err
	}
	n.Content = append(n.Content, keyNode(key), &vn)
	return nil
}

// appendScalar appends a string-valued key to a mapping node.
func appendScalar(n *yaml.Node, key, val string) {
	n.Content = append(n.Content, keyNode(key), &yaml.Node{Kind: yaml.ScalarNode, Value: val})
}

func appendOptInt(n *yaml.Node, key string, v *int64) error {
	if v == nil {
		return nil
	}
	return appendField(n, key, *v)
}

func appendOptFloat(n *yaml.Node, key string, v *float64) error {
	if v == nil {
		return nil
	}
	return appendField(n, key, *v)
}

func keyNode(k string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: k}
}

// appendSortedX appends preserved x_ vendor extensions in sorted key order, so
// the emitted file is deterministic and does not churn across runs (RFC §11).
func appendSortedX(n *yaml.Node, x map[string]any) error {
	if len(x) == 0 {
		return nil
	}
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := appendField(n, k, x[k]); err != nil {
			return err
		}
	}
	return nil
}
