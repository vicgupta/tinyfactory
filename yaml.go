// yaml.go — a minimal YAML-subset parser.
//
// It parses exactly the constructs used by factory-flow.yaml and errors
// clearly on anything else. Supported: full-line and trailing comments,
// nested maps by indentation (spaces only), block sequences of scalars
// ("- item"), inline flow lists ("[a, b]"), and scalars (plain,
// single/double-quoted, booleans, integers, null). Not supported: anchors,
// aliases, flow maps, block scalars (| and >), tabs, multi-line values,
// maps inside sequences. Anything unsupported is an error, never silently
// ignored.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

// NodeKind is the kind of a parsed YAML node.
type NodeKind int

const (
	KindScalar NodeKind = iota
	KindMap
	KindList
)

func (k NodeKind) String() string {
	switch k {
	case KindScalar:
		return "scalar"
	case KindMap:
		return "map"
	case KindList:
		return "list"
	default:
		return "unknown"
	}
}

// Pair is one key/value pair of a mapping, kept in document order.
type Pair struct {
	Key   string
	Value *Node
}

// Node is one parsed YAML value. Maps keep their pairs in document order
// so callers can validate key order (for example the stage order).
type Node struct {
	Kind   NodeKind
	Scalar string  // raw scalar text for KindScalar (unquoted); "" for null
	Null   bool    // true when the scalar is empty, "~", or "null"
	Pairs  []Pair  // KindMap, document order
	Items  []*Node // KindList
}

// Get returns the value for key in a map node, or nil when absent or when
// the node is not a map.
func (n *Node) Get(key string) *Node {
	if n == nil || n.Kind != KindMap {
		return nil
	}
	for _, p := range n.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return nil
}

// IsNull reports whether the node is a null scalar.
func (n *Node) IsNull() bool {
	return n != nil && n.Kind == KindScalar && n.Null
}

// AsString returns the scalar text, or an error when the node is not a
// non-null scalar.
func (n *Node) AsString() (string, error) {
	if n == nil {
		return "", fmt.Errorf("missing value")
	}
	if n.Kind != KindScalar {
		return "", fmt.Errorf("expected a scalar, got %s", n.Kind)
	}
	if n.Null {
		return "", fmt.Errorf("value is null")
	}
	return n.Scalar, nil
}

// AsBool parses the scalar as a YAML boolean.
func (n *Node) AsBool() (bool, error) {
	s, err := n.AsString()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(s) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("expected a boolean, got %q", s)
}

// AsInt parses the scalar as an integer.
func (n *Node) AsInt() (int, error) {
	s, err := n.AsString()
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("expected an integer, got %q", s)
	}
	return v, nil
}

// AsStringList returns the items of a list node as strings.
func (n *Node) AsStringList() ([]string, error) {
	if n == nil {
		return nil, fmt.Errorf("missing value")
	}
	if n.Kind != KindList {
		return nil, fmt.Errorf("expected a list, got %s", n.Kind)
	}
	out := make([]string, 0, len(n.Items))
	for _, it := range n.Items {
		s, err := it.AsString()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ParseYAML parses data as the supported YAML subset.
func ParseYAML(data []byte) (*Node, error) {
	lines, err := splitYAMLlines(data)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	p := &yamlParser{lines: lines}
	node, err := p.parseBlock(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation or content", p.lines[p.pos].num)
	}
	return node, nil
}

type yamlLine struct {
	num     int // 1-based line number in the source
	indent  int // leading spaces
	content string
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

// splitYAMLlines splits the input into significant lines: blank lines and
// full-line comments are dropped, trailing comments are stripped, and a
// tab in the leading whitespace is an error.
func splitYAMLlines(data []byte) ([]yamlLine, error) {
	var out []yamlLine
	raw := strings.Split(string(data), "\n")
	for i, r := range raw {
		r = strings.TrimSuffix(r, "\r")
		if strings.TrimSpace(r) == "" {
			continue
		}
		indent := 0
		for indent < len(r) && r[indent] == ' ' {
			indent++
		}
		if indent < len(r) && r[indent] == '\t' {
			return nil, fmt.Errorf("line %d: tabs are not allowed for indentation", i+1)
		}
		content := stripYAMLComment(r[indent:])
		content = strings.TrimRight(content, " \t")
		if content == "" {
			continue // full-line comment
		}
		out = append(out, yamlLine{num: i + 1, indent: indent, content: content})
	}
	return out, nil
}

// stripYAMLComment cuts a trailing comment: a '#' that is not inside quotes
// and is at the start of the text or preceded by whitespace.
func stripYAMLComment(s string) string {
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

func nullNode() *Node {
	return &Node{Kind: KindScalar, Null: true}
}

// parseBlock parses the block starting at the current line. The block is a
// sequence when the first line is a sequence item, otherwise a mapping.
func (p *yamlParser) parseBlock(minIndent int) (*Node, error) {
	if p.pos >= len(p.lines) {
		return nullNode(), nil
	}
	line := p.lines[p.pos]
	if line.indent < minIndent {
		return nullNode(), nil
	}
	if isSequenceItem(line.content) {
		return p.parseSequence(line.indent)
	}
	return p.parseMap(line.indent)
}

func isSequenceItem(content string) bool {
	return content == "-" || strings.HasPrefix(content, "- ")
}

// parseMap parses a mapping whose keys all sit at indent.
func (p *yamlParser) parseMap(indent int) (*Node, error) {
	node := &Node{Kind: KindMap}
	seen := map[string]bool{}
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent != indent {
			break
		}
		if isSequenceItem(line.content) {
			return nil, fmt.Errorf("line %d: sequence item inside a mapping", line.num)
		}
		key, valueText, ok := splitKeyValue(line.content)
		if !ok {
			return nil, fmt.Errorf("line %d: expected 'key: value', got %q", line.num, line.content)
		}
		if seen[key] {
			return nil, fmt.Errorf("line %d: duplicate key %q", line.num, key)
		}
		seen[key] = true
		p.pos++
		var val *Node
		var err error
		if valueText == "" {
			val, err = p.parseNestedValue(indent, line.num)
			if err != nil {
				return nil, err
			}
		} else {
			val, err = parseYAMLValue(valueText, line.num)
			if err != nil {
				return nil, err
			}
		}
		node.Pairs = append(node.Pairs, Pair{Key: key, Value: val})
	}
	if len(node.Pairs) == 0 {
		return nil, fmt.Errorf("line %d: expected a mapping", p.lines[p.pos].num)
	}
	return node, nil
}

// parseNestedValue parses the value of a "key:" line with no inline value:
// a deeper-indented block, a sequence at the same indent as the key, or
// null when nothing follows.
func (p *yamlParser) parseNestedValue(keyIndent, keyLine int) (*Node, error) {
	if p.pos >= len(p.lines) {
		return nullNode(), nil
	}
	next := p.lines[p.pos]
	if next.indent > keyIndent {
		return p.parseBlock(next.indent)
	}
	if next.indent == keyIndent && isSequenceItem(next.content) {
		return p.parseSequence(keyIndent)
	}
	return nullNode(), nil
}

// parseSequence parses a block sequence whose items all sit at indent.
// Items must be scalars (or flow lists) on the same line; maps and nested
// blocks inside a sequence are not supported.
func (p *yamlParser) parseSequence(indent int) (*Node, error) {
	node := &Node{Kind: KindList}
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.indent != indent || !isSequenceItem(line.content) {
			break
		}
		itemText := strings.TrimSpace(strings.TrimPrefix(line.content, "-"))
		if itemText == "" {
			return nil, fmt.Errorf("line %d: sequence items must be scalars on the same line", line.num)
		}
		if _, _, ok := splitKeyValue(itemText); ok {
			return nil, fmt.Errorf("line %d: maps inside sequences are not supported: %q", line.num, itemText)
		}
		item, err := parseYAMLValue(itemText, line.num)
		if err != nil {
			return nil, err
		}
		node.Items = append(node.Items, item)
		p.pos++
	}
	return node, nil
}

// splitKeyValue splits "key: value" or "key:" into its parts. The split is
// at the first unquoted ": " or at a trailing ":". It reports false when
// the line is not a mapping entry.
func splitKeyValue(content string) (key, value string, ok bool) {
	var inSingle, inDouble bool
	for i := 0; i < len(content); i++ {
		c := content[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < len(content) && content[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == ':':
			if i == len(content)-1 {
				return parseYAMLKey(content[:i]), "", true
			}
			if i+1 < len(content) && (content[i+1] == ' ' || content[i+1] == '\t') {
				return parseYAMLKey(content[:i]), strings.TrimSpace(content[i+1:]), true
			}
		}
	}
	return "", "", false
}

// parseYAMLKey parses a mapping key: plain text, or a quoted string.
func parseYAMLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && (raw[0] == '\'' || raw[0] == '"') {
		if v, err := parseYAMLQuoted(raw, 0); err == nil {
			return v
		}
	}
	return raw
}

// parseYAMLValue parses one scalar or flow-list value on a single line.
func parseYAMLValue(text string, lineNum int) (*Node, error) {
	if text == "" || text == "~" || strings.EqualFold(text, "null") {
		return nullNode(), nil
	}
	switch text[0] {
	case '[':
		return parseYAMLFlowList(text, lineNum)
	case '{':
		return nil, fmt.Errorf("line %d: flow maps are not supported: %q", lineNum, text)
	case '|', '>':
		return nil, fmt.Errorf("line %d: block scalars are not supported: %q", lineNum, text)
	case '&', '*':
		return nil, fmt.Errorf("line %d: anchors and aliases are not supported: %q", lineNum, text)
	case '\'', '"':
		v, err := parseYAMLQuoted(text, lineNum)
		if err != nil {
			return nil, err
		}
		return &Node{Kind: KindScalar, Scalar: v}, nil
	}
	if strings.Contains(text, ": ") || strings.HasSuffix(text, ":") {
		return nil, fmt.Errorf("line %d: unexpected ':' in scalar %q", lineNum, text)
	}
	switch text[0] {
	case '#', '!', '%', '@', '`', ',', '?':
		return nil, fmt.Errorf("line %d: unsupported scalar %q", lineNum, text)
	}
	return &Node{Kind: KindScalar, Scalar: text}, nil
}

// parseYAMLFlowList parses "[a, b, c]" on one line.
func parseYAMLFlowList(text string, lineNum int) (*Node, error) {
	if !strings.HasSuffix(text, "]") {
		return nil, fmt.Errorf("line %d: flow list is not closed on one line: %q", lineNum, text)
	}
	inner := strings.TrimSpace(text[1 : len(text)-1])
	node := &Node{Kind: KindList}
	if inner == "" {
		return node, nil
	}
	parts, err := splitFlowItems(inner, lineNum)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		item, err := parseYAMLValue(strings.TrimSpace(part), lineNum)
		if err != nil {
			return nil, err
		}
		node.Items = append(node.Items, item)
	}
	return node, nil
}

// splitFlowItems splits a flow-list body on commas that are not inside
// quotes.
func splitFlowItems(s string, lineNum int) ([]string, error) {
	var parts []string
	var inSingle, inDouble bool
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == ',':
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("line %d: unterminated quote in flow list", lineNum)
	}
	parts = append(parts, s[start:])
	return parts, nil
}

// parseYAMLQuoted parses a quoted scalar that must span the whole text.
func parseYAMLQuoted(text string, lineNum int) (string, error) {
	quote := text[0]
	var b strings.Builder
	i := 1
	for i < len(text) {
		c := text[i]
		if quote == '\'' {
			if c == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				if i != len(text)-1 {
					return "", fmt.Errorf("line %d: unexpected content after quoted scalar", lineNum)
				}
				return b.String(), nil
			}
			b.WriteByte(c)
			i++
			continue
		}
		// Double-quoted.
		if c == '\\' {
			if i+1 >= len(text) {
				return "", fmt.Errorf("line %d: unterminated escape in quoted scalar", lineNum)
			}
			switch text[i+1] {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", fmt.Errorf("line %d: unsupported escape \\%c in quoted scalar", lineNum, text[i+1])
			}
			i += 2
			continue
		}
		if c == '"' {
			if i != len(text)-1 {
				return "", fmt.Errorf("line %d: unexpected content after quoted scalar", lineNum)
			}
			return b.String(), nil
		}
		b.WriteByte(c)
		i++
	}
	return "", fmt.Errorf("line %d: unterminated quoted scalar", lineNum)
}
