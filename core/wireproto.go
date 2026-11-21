package core

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// wireField represents a single field entry to be encoded onto the protobuf wire.
type wireField struct {
	num      protowire.Number
	wireType protowire.Type
	varint   uint64
	bytesVal []byte
}

func wfVarint(num protowire.Number, v uint64) wireField {
	return wireField{num: num, wireType: protowire.VarintType, varint: v}
}

func wfString(num protowire.Number, s string) wireField {
	return wireField{num: num, wireType: protowire.BytesType, bytesVal: []byte(s)}
}

func wfBytes(num protowire.Number, b []byte) wireField {
	return wireField{num: num, wireType: protowire.BytesType, bytesVal: b}
}

func wfMsg(num protowire.Number, fields ...wireField) wireField {
	return wireField{num: num, wireType: protowire.BytesType, bytesVal: encodeWireMsg(fields...)}
}

func wfEmptyMsg(num protowire.Number) wireField {
	return wireField{num: num, wireType: protowire.BytesType, bytesVal: nil}
}

func wfRepeatedVarint(num protowire.Number, values ...uint64) []wireField {
	out := make([]wireField, 0, len(values))
	for _, v := range values {
		out = append(out, wfVarint(num, v))
	}
	return out
}

func wfRepeatedString(num protowire.Number, values ...string) []wireField {
	out := make([]wireField, 0, len(values))
	for _, v := range values {
		out = append(out, wfString(num, v))
	}
	return out
}

func encodeWireMsg(fields ...wireField) []byte {
	var out []byte
	for _, f := range fields {
		out = protowire.AppendTag(out, f.num, f.wireType)
		switch f.wireType {
		case protowire.VarintType:
			out = protowire.AppendVarint(out, f.varint)
		case protowire.BytesType:
			out = protowire.AppendBytes(out, f.bytesVal)
		}
	}
	return out
}

// wireValue stores a single decoded raw protobuf field value.
type wireValue struct {
	wireType protowire.Type
	varint   uint64
	fixed32  uint32
	fixed64  uint64
	bytesVal []byte
}

// wireNode maps protobuf field numbers to their raw values in wire order.
type wireNode map[protowire.Number][]wireValue

func decodeWireMsg(b []byte) (wireNode, error) {
	node := make(wireNode)
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("decode protobuf tag: %w", protowire.ParseError(n))
		}
		b = b[n:]

		var val wireValue
		val.wireType = typ
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return nil, fmt.Errorf("decode varint field %d: %w", num, protowire.ParseError(m))
			}
			val.varint = v
			b = b[m:]
		case protowire.Fixed32Type:
			v, m := protowire.ConsumeFixed32(b)
			if m < 0 {
				return nil, fmt.Errorf("decode fixed32 field %d: %w", num, protowire.ParseError(m))
			}
			val.fixed32 = v
			b = b[m:]
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				return nil, fmt.Errorf("decode fixed64 field %d: %w", num, protowire.ParseError(m))
			}
			val.fixed64 = v
			b = b[m:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, fmt.Errorf("decode bytes field %d: %w", num, protowire.ParseError(m))
			}
			val.bytesVal = v
			b = b[m:]
		case protowire.StartGroupType:
			v, m := protowire.ConsumeGroup(num, b)
			if m < 0 {
				return nil, fmt.Errorf("decode group field %d: %w", num, protowire.ParseError(m))
			}
			val.bytesVal = v
			b = b[m:]
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d for field %d", typ, num)
		}

		node[num] = append(node[num], val)
	}
	return node, nil
}

func (n wireNode) has(num protowire.Number) bool {
	if n == nil {
		return false
	}
	return len(n[num]) > 0
}

func (n wireNode) firstVarint(num protowire.Number) (int64, bool) {
	if n == nil {
		return 0, false
	}
	for _, v := range n[num] {
		if v.wireType == protowire.VarintType {
			return int64(v.varint), true
		}
	}
	return 0, false
}

func (n wireNode) firstBytes(num protowire.Number) ([]byte, bool) {
	if n == nil {
		return nil, false
	}
	for _, v := range n[num] {
		if v.wireType == protowire.BytesType {
			return v.bytesVal, true
		}
	}
	return nil, false
}

func (n wireNode) firstString(num protowire.Number) (string, bool) {
	b, ok := n.firstBytes(num)
	if !ok {
		return "", false
	}
	return string(b), true
}

func (n wireNode) firstMsg(num protowire.Number) (wireNode, bool) {
	b, ok := n.firstBytes(num)
	if !ok {
		return nil, false
	}
	child, err := decodeWireMsg(b)
	if err != nil {
		return nil, false
	}
	return child, true
}

func (n wireNode) allMsgs(num protowire.Number) []wireNode {
	if n == nil {
		return nil
	}
	vals := n[num]
	if len(vals) == 0 {
		return nil
	}
	out := make([]wireNode, 0, len(vals))
	for _, v := range vals {
		if v.wireType != protowire.BytesType {
			continue
		}
		if child, err := decodeWireMsg(v.bytesVal); err == nil {
			out = append(out, child)
		}
	}
	return out
}

func (n wireNode) nestedMsg(path ...protowire.Number) (wireNode, bool) {
	curr := n
	for _, num := range path {
		var ok bool
		curr, ok = curr.firstMsg(num)
		if !ok {
			return nil, false
		}
	}
	return curr, true
}

func (n wireNode) nestedString(path ...protowire.Number) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	parent, ok := n.nestedMsg(path[:len(path)-1]...)
	if !ok {
		return "", false
	}
	return parent.firstString(path[len(path)-1])
}

func (n wireNode) nestedBytes(path ...protowire.Number) ([]byte, bool) {
	if len(path) == 0 {
		return nil, false
	}
	parent, ok := n.nestedMsg(path[:len(path)-1]...)
	if !ok {
		return nil, false
	}
	return parent.firstBytes(path[len(path)-1])
}

func (n wireNode) nestedVarint(path ...protowire.Number) (int64, bool) {
	if len(path) == 0 {
		return 0, false
	}
	parent, ok := n.nestedMsg(path[:len(path)-1]...)
	if !ok {
		return 0, false
	}
	return parent.firstVarint(path[len(path)-1])
}
