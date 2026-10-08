package lime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// JSONPatch applies RFC 6902 to an owned document and preserves numeric literals.
// A failed batch returns no output and never changes either input. Streaming
// assemblers retain the parsed tree between contributions; this helper is for
// one-shot use. Default profile size/depth/operation limits apply.
func JSONPatch(target, patch []byte) (json.RawMessage, error) {
	l, _ := (Limits{}).normalized()
	d, err := newPatchDocument(target, l.ContentBytes)
	if err != nil {
		return nil, err
	}
	if err = d.apply(patch, l.Entries); err != nil {
		return nil, err
	}
	return d.finish()
}

type patchNode struct {
	object map[string]*patchNode
	array  []*patchNode
	scalar []byte
	size   int
}
type patchDocument struct {
	root     *patchNode
	maxBytes int
}

func parseNode(raw []byte, depth int) (*patchNode, error) {
	if err := strictJSON(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return buildNode(value, depth)
}
func buildNode(value any, depth int) (*patchNode, error) {
	if depth > MaxDepth {
		return nil, errors.New("JSON Patch nesting limit exceeded")
	}
	n := &patchNode{size: 2}
	switch value := value.(type) {
	case map[string]any:
		n.object = make(map[string]*patchNode, len(value))
		for key, value := range value {
			child, err := buildNode(value, depth+1)
			if err != nil {
				return nil, err
			}
			n.object[key] = child
			n.size += len(quote(nil, key)) + 1 + child.size
		}
		if len(value) > 0 {
			n.size += len(value) - 1
		}
	case []any:
		n.array = make([]*patchNode, 0, len(value))
		for _, value := range value {
			child, err := buildNode(value, depth+1)
			if err != nil {
				return nil, err
			}
			n.array = append(n.array, child)
			n.size += child.size
		}
		if len(value) > 0 {
			n.size += len(value) - 1
		}
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		n.scalar = raw
		n.size = len(raw)
	}
	return n, nil
}
func newPatchDocument(raw []byte, maxBytes int) (*patchDocument, error) {
	root, err := parseNode(raw, 0)
	if err != nil {
		return nil, err
	}
	if root.size > maxBytes {
		return nil, errors.New("assembled JSON limit exceeded")
	}
	return &patchDocument{root: root, maxBytes: maxBytes}, nil
}
func appendNode(dst []byte, n *patchNode) []byte {
	if n.object != nil {
		dst = append(dst, '{')
		keys := make([]string, 0, len(n.object))
		for key := range n.object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for i, key := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = quote(dst, key)
			dst = append(dst, ':')
			dst = appendNode(dst, n.object[key])
		}
		return append(dst, '}')
	}
	if n.array != nil {
		dst = append(dst, '[')
		for i, child := range n.array {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendNode(dst, child)
		}
		return append(dst, ']')
	}
	return append(dst, n.scalar...)
}
func (d *patchDocument) finish() (json.RawMessage, error) {
	if d.root == nil {
		return nil, errors.New("JSON Patch removed the root without replacing it")
	}
	return appendNode(make([]byte, 0, d.root.size), d.root), nil
}
func patchPointer(raw json.RawMessage) ([]string, error) {
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return nil, errors.New("invalid JSON Pointer")
	}
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "/") {
		return nil, errors.New("invalid JSON Pointer")
	}
	tokens := strings.Split(value[1:], "/")
	for i, token := range tokens {
		for j := 0; j < len(token); j++ {
			if token[j] == '~' {
				j++
				if j == len(token) || (token[j] != '0' && token[j] != '1') {
					return nil, errors.New("invalid JSON Pointer escape")
				}
			}
		}
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return tokens, nil
}
func patchIndex(key string, length int, add bool) (int, error) {
	if add && key == "-" {
		return length, nil
	}
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, errors.New("invalid array index")
	}
	for _, c := range key {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid array index")
		}
	}
	index, err := strconv.Atoi(key)
	if err != nil || index > length || (!add && index == length) {
		return 0, errors.New("array index out of bounds")
	}
	return index, nil
}
func (d *patchDocument) location(path []string) (*patchNode, string, []*patchNode, error) {
	parent := d.root
	ancestors := make([]*patchNode, 0, len(path))
	for i, token := range path {
		if parent == nil || (parent.object == nil && parent.array == nil) {
			return nil, "", nil, errors.New("JSON Pointer parent does not exist")
		}
		ancestors = append(ancestors, parent)
		if i == len(path)-1 {
			return parent, token, ancestors, nil
		}
		if parent.array != nil {
			index, err := patchIndex(token, len(parent.array), false)
			if err != nil {
				return nil, "", nil, err
			}
			parent = parent.array[index]
		} else {
			parent = parent.object[token]
		}
	}
	return nil, "", nil, errors.New("JSON Pointer has no parent")
}
func (d *patchDocument) get(path []string) (*patchNode, error) {
	if len(path) == 0 {
		if d.root == nil {
			return nil, errors.New("JSON Pointer target does not exist")
		}
		return d.root, nil
	}
	parent, key, _, err := d.location(path)
	if err != nil {
		return nil, err
	}
	if parent.array != nil {
		index, err := patchIndex(key, len(parent.array), false)
		if err != nil {
			return nil, err
		}
		return parent.array[index], nil
	}
	child := parent.object[key]
	if child == nil {
		return nil, errors.New("JSON Pointer target does not exist")
	}
	return child, nil
}
func (d *patchDocument) write(path []string, op string, value *patchNode) error {
	if len(path) == 0 {
		if op != "add" {
			if _, err := d.get(path); err != nil {
				return err
			}
		}
		if value != nil && value.size > d.maxBytes {
			return errors.New("assembled JSON limit exceeded")
		}
		d.root = value
		return nil
	}
	parent, key, ancestors, err := d.location(path)
	if err != nil {
		return err
	}
	delta := 0
	if parent.array != nil {
		index, err := patchIndex(key, len(parent.array), op == "add")
		if err != nil {
			return err
		}
		switch op {
		case "add":
			delta = value.size
			if len(parent.array) > 0 {
				delta++
			}
		case "remove":
			delta = -parent.array[index].size
			if len(parent.array) > 1 {
				delta--
			}
		default:
			delta = value.size - parent.array[index].size
		}
		if d.root.size+delta > d.maxBytes {
			return errors.New("assembled JSON limit exceeded")
		}
		switch op {
		case "add":
			parent.array = append(parent.array, nil)
			copy(parent.array[index+1:], parent.array[index:])
			parent.array[index] = value
		case "remove":
			copy(parent.array[index:], parent.array[index+1:])
			parent.array[len(parent.array)-1] = nil
			parent.array = parent.array[:len(parent.array)-1]
		default:
			parent.array[index] = value
		}
	} else {
		old := parent.object[key]
		if op != "add" && old == nil {
			return errors.New("JSON Pointer target does not exist")
		}
		if op == "remove" {
			delta = -old.size - len(quote(nil, key)) - 1
			if len(parent.object) > 1 {
				delta--
			}
		} else {
			delta = value.size
			if old != nil {
				delta -= old.size
			} else {
				delta += len(quote(nil, key)) + 1
				if len(parent.object) > 0 {
					delta++
				}
			}
		}
		if d.root.size+delta > d.maxBytes {
			return errors.New("assembled JSON limit exceeded")
		}
		if op == "remove" {
			delete(parent.object, key)
		} else {
			parent.object[key] = value
		}
	}
	for _, ancestor := range ancestors {
		ancestor.size += delta
	}
	return nil
}
func cloneNode(n *patchNode, depth int) (*patchNode, error) {
	if depth > MaxDepth {
		return nil, errors.New("JSON Patch nesting limit exceeded")
	}
	out := &patchNode{size: n.size, scalar: n.scalar}
	if n.object != nil {
		out.object = make(map[string]*patchNode, len(n.object))
		for k, v := range n.object {
			child, err := cloneNode(v, depth+1)
			if err != nil {
				return nil, err
			}
			out.object[k] = child
		}
	}
	if n.array != nil {
		out.array = make([]*patchNode, len(n.array))
		for i, v := range n.array {
			child, err := cloneNode(v, depth+1)
			if err != nil {
				return nil, err
			}
			out.array[i] = child
		}
	}
	return out, nil
}

// Compare decimal numbers without float rounding or allocating a power of ten
// for an attacker-controlled exponent. Coefficients and exponents stay strings.
func decimal(raw []byte) (string, string) {
	value := string(raw)
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == 'e' || r == 'E' })
	exponent := new(big.Int)
	if len(parts) > 1 {
		exponent.SetString(parts[1], 10)
	}
	mantissa := parts[0]
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(mantissa)-dot-1)))
		mantissa = strings.ReplaceAll(mantissa, ".", "")
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0", "0"
	}
	trimmed := strings.TrimRight(mantissa, "0")
	exponent.Add(exponent, big.NewInt(int64(len(mantissa)-len(trimmed))))
	if negative {
		trimmed = "-" + trimmed
	}
	return trimmed, exponent.String()
}
func equalNode(a, b *patchNode) bool {
	if a.object != nil || b.object != nil {
		if a.object == nil || b.object == nil || len(a.object) != len(b.object) {
			return false
		}
		for k, v := range a.object {
			other := b.object[k]
			if other == nil || !equalNode(v, other) {
				return false
			}
		}
		return true
	}
	if a.array != nil || b.array != nil {
		if a.array == nil || b.array == nil || len(a.array) != len(b.array) {
			return false
		}
		for i, v := range a.array {
			if !equalNode(v, b.array[i]) {
				return false
			}
		}
		return true
	}
	if (a.scalar[0] == '-' || (a.scalar[0] >= '0' && a.scalar[0] <= '9')) && (b.scalar[0] == '-' || (b.scalar[0] >= '0' && b.scalar[0] <= '9')) {
		ac, ae := decimal(a.scalar)
		bc, be := decimal(b.scalar)
		return ac == bc && ae == be
	}
	return bytes.Equal(a.scalar, b.scalar)
}
func (d *patchDocument) apply(raw []byte, maxOperations int) error {
	if len(raw) > d.maxBytes {
		return errors.New("JSON Patch contribution limit exceeded")
	}
	if err := strictJSON(raw); err != nil {
		return err
	}
	if bytes.TrimSpace(raw)[0] != '[' {
		return errors.New("JSON Patch must be an operation array")
	}
	var operations []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &operations); err != nil {
		return err
	}
	if len(operations) > maxOperations {
		return errors.New("JSON Patch operation limit exceeded")
	}
	copied := 0
	for i, operation := range operations {
		err := d.operation(operation, &copied)
		if err != nil {
			return fmt.Errorf("JSON Patch operation %d: %w", i, err)
		}
	}
	return nil
}
func (d *patchDocument) operation(operation map[string]json.RawMessage, copied *int) error {
	var op string
	if json.Unmarshal(operation["op"], &op) != nil {
		return errors.New("invalid JSON Patch operation")
	}
	path, err := patchPointer(operation["path"])
	if err != nil {
		return err
	}
	switch op {
	case "add", "replace", "test":
		raw, ok := operation["value"]
		if !ok {
			return errors.New("JSON Patch operation requires value")
		}
		value, err := parseNode(raw, len(path))
		if err != nil {
			return err
		}
		if op == "test" {
			target, err := d.get(path)
			if err != nil {
				return err
			}
			if !equalNode(target, value) {
				return errors.New("JSON Patch test failed")
			}
			return nil
		}
		return d.write(path, op, value)
	case "remove":
		return d.write(path, op, nil)
	case "copy", "move":
		from, err := patchPointer(operation["from"])
		if err != nil {
			return err
		}
		source, err := d.get(from)
		if err != nil {
			return err
		}
		if op == "move" && len(from) < len(path) {
			prefix := true
			for i, token := range from {
				if token != path[i] {
					prefix = false
					break
				}
			}
			if prefix {
				return errors.New("cannot move a value into its descendant")
			}
		}
		*copied += source.size
		if *copied > d.maxBytes {
			return errors.New("JSON Patch copy work limit exceeded")
		}
		value, err := cloneNode(source, len(path))
		if err != nil {
			return err
		}
		if op == "move" {
			if err := d.write(from, "remove", nil); err != nil {
				return err
			}
		}
		return d.write(path, "add", value)
	default:
		return errors.New("unknown JSON Patch operation")
	}
}
