package obx

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
)

// Extension members (docs/protocol.md section 2, docs/ingest.md 3.1): a
// member named x-<vendor>-<name> holds any JSON value. A node stores the
// ones a brand writes and returns them unchanged; consumers ignore the ones
// they do not know. Every type whose schema allows them has a field X that
// keeps them, so a document read with these types and written again keeps
// its extensions.

var extensionKey = regexp.MustCompile(`^x-[a-z0-9]+-[a-z0-9_-]+$`)

// IsExtensionKey reports whether a member name is an extension
// (^x-[a-z0-9]+-[a-z0-9_-]+$).
func IsExtensionKey(k string) bool { return extensionKey.MatchString(k) }

// Extensions are the x-<vendor>-<name> members of an object, by name, with
// their JSON values. nil or empty means none.
type Extensions map[string]json.RawMessage

// Clone returns a copy (nil when empty).
func (x Extensions) Clone() Extensions {
	if len(x) == 0 {
		return nil
	}
	out := make(Extensions, len(x))
	for k, v := range x {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

// marshalExt marshals plain (a type without the methods of its original)
// and appends the extension members, sorted by name.
func marshalExt(plain any, x Extensions) ([]byte, error) {
	b, err := json.Marshal(plain)
	if err != nil || len(x) == 0 {
		return b, err
	}
	keys := make([]string, 0, len(x))
	for k := range x {
		if IsExtensionKey(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return b, nil
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1])
	empty := bytes.Equal(bytes.TrimSpace(b), []byte("{}"))
	for i, k := range keys {
		if i > 0 || !empty {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		v := x[k]
		if len(v) == 0 {
			v = json.RawMessage("null")
		}
		var c bytes.Buffer
		if err := json.Compact(&c, v); err != nil {
			return nil, err
		}
		buf.Write(c.Bytes())
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// unmarshalExt decodes data into plain and returns its extension members.
func unmarshalExt(data []byte, plain any) (Extensions, error) {
	if err := json.Unmarshal(data, plain); err != nil {
		return nil, err
	}
	if !bytes.Contains(data, []byte(`"x-`)) {
		return nil, nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, nil // not an object (null): no extensions
	}
	var x Extensions
	for k, v := range all {
		if IsExtensionKey(k) {
			if x == nil {
				x = Extensions{}
			}
			x[k] = v
		}
	}
	return x, nil
}
