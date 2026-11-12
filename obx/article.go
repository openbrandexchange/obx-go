package obx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Article data added in 0.2 (docs/protocol.md section 5.9). New product and
// variant properties are always present in protocol objects (null when not
// stated); inside Measurements and Features only the stated members are
// present, and the fibre flags Recycled and Organic only when true.

// NormalizeFiber turns a 0.1 fibre key recycled_<fibre> or organic_<fibre>
// into the fibre with the flag set (docs/protocol.md 5.9.1): recycled_polyester
// becomes polyester with Recycled, organic_cotton becomes cotton with Organic.
// The prefixes may be combined (organic_recycled_cotton).
func NormalizeFiber(f Fiber) Fiber {
	for {
		switch {
		case strings.HasPrefix(f.Fiber, "recycled_") && len(f.Fiber) > len("recycled_"):
			f.Fiber, f.Recycled = strings.TrimPrefix(f.Fiber, "recycled_"), true
		case strings.HasPrefix(f.Fiber, "organic_") && len(f.Fiber) > len("organic_"):
			f.Fiber, f.Organic = strings.TrimPrefix(f.Fiber, "organic_"), true
		default:
			return f
		}
	}
}

// Normalized returns a copy of m with every fibre normalised (NormalizeFiber).
// A 0.2 node stores and returns only normalised fibres.
func (m Materials) Normalized() Materials {
	if m == nil {
		return nil
	}
	out := make(Materials, len(m))
	for i, p := range m {
		q := MaterialPart{Part: p.Part}
		if p.Fibers != nil {
			q.Fibers = make([]Fiber, len(p.Fibers))
			for j, f := range p.Fibers {
				q.Fibers[j] = NormalizeFiber(f)
			}
		}
		out[i] = q
	}
	return out
}

// Footwear material categories of Directive 94/11/EC.
const (
	FootwearLeather       = "leather"
	FootwearCoatedLeather = "coated_leather"
	FootwearTextile       = "textile"
	FootwearOther         = "other"
)

// FootwearMaterialValues lists the four categories.
var FootwearMaterialValues = []string{FootwearLeather, FootwearCoatedLeather, FootwearTextile, FootwearOther}

// FootwearParts are the three parts of a shoe, in label order.
var FootwearParts = []string{"upper", "lining_and_sock", "outer_sole"}

// FootwearMaterials is the material of the three parts of a shoe
// (schemas/FootwearMaterials.json). Responses include all three parts; nil
// means not stated.
type FootwearMaterials struct {
	Upper         *string `json:"upper"`
	LiningAndSock *string `json:"lining_and_sock"`
	OuterSole     *string `json:"outer_sole"`
}

// Part returns the value of a part by key ("upper", "lining_and_sock",
// "outer_sole"), or nil.
func (f *FootwearMaterials) Part(key string) *string {
	if f == nil {
		return nil
	}
	switch key {
	case "upper":
		return f.Upper
	case "lining_and_sock":
		return f.LiningAndSock
	case "outer_sole":
		return f.OuterSole
	}
	return nil
}

// SetPart sets a part by key; it reports false for an unknown key.
func (f *FootwearMaterials) SetPart(key string, v *string) bool {
	switch key {
	case "upper":
		f.Upper = v
	case "lining_and_sock":
		f.LiningAndSock = v
	case "outer_sole":
		f.OuterSole = v
	default:
		return false
	}
	return true
}

// Complete reports whether all three parts are stated.
func (f *FootwearMaterials) Complete() bool {
	return f != nil && f.Upper != nil && f.LiningAndSock != nil && f.OuterSole != nil
}

// Certification scopes.
const (
	CertificationScopeProduct  = "product"
	CertificationScopeMaterial = "material"
)

// Certification is one voluntary certification the brand states
// (schemas/Certifications.json). The node passes it on unchecked.
type Certification struct {
	Scheme        string  `json:"scheme"`
	LicenceNumber *string `json:"licence_number"`
	Scope         string  `json:"scope"`
}

// Colour families (schemas/Color.json, docs/protocol.md 5.9.5).
var ColorFamilies = []string{"black", "white", "grey", "beige", "brown", "red", "pink", "orange", "yellow", "green", "blue", "purple", "gold", "silver", "multicolour", "transparent"}

// Range is a shortest and longest setting, as used by
// Measurements.StrapLengthCM.
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Measurements of one variant (schemas/Measurements.json). The unit is part
// of the key; nil members are not stated and left out of the JSON.
type Measurements struct {
	HeightCM        *float64 `json:"height_cm,omitempty"`
	WidthCM         *float64 `json:"width_cm,omitempty"`
	DepthCM         *float64 `json:"depth_cm,omitempty"`
	WeightKG        *float64 `json:"weight_kg,omitempty"`
	VolumeL         *float64 `json:"volume_l,omitempty"`
	VolumeExpandedL *float64 `json:"volume_expanded_l,omitempty"`
	HandleDropCM    *float64 `json:"handle_drop_cm,omitempty"`
	StrapLengthCM   *Range   `json:"strap_length_cm,omitempty"`
	LaptopMaxInch   *float64 `json:"laptop_max_inch,omitempty"`
	HeelHeightCM    *float64 `json:"heel_height_cm,omitempty"`
	ShaftHeightCM   *float64 `json:"shaft_height_cm,omitempty"`
	InseamCM        *float64 `json:"inseam_cm,omitempty"`
}

// MeasurementKeys lists the measurement keys in the order of the schema
// (and of the products.csv columns).
var MeasurementKeys = []string{"height_cm", "width_cm", "depth_cm", "weight_kg", "volume_l", "volume_expanded_l", "handle_drop_cm", "strap_length_cm", "laptop_max_inch", "heel_height_cm", "shaft_height_cm", "inseam_cm"}

// Number returns the numeric measurement with the given key (every key but
// strap_length_cm), or nil.
func (m *Measurements) Number(key string) *float64 {
	if p := m.numberField(key); p != nil {
		return *p
	}
	return nil
}

// SetNumber sets a numeric measurement by key; it reports false for an
// unknown key and for strap_length_cm.
func (m *Measurements) SetNumber(key string, v *float64) bool {
	p := m.numberField(key)
	if p == nil {
		return false
	}
	*p = v
	return true
}

func (m *Measurements) numberField(key string) **float64 {
	if m == nil {
		return nil
	}
	switch key {
	case "height_cm":
		return &m.HeightCM
	case "width_cm":
		return &m.WidthCM
	case "depth_cm":
		return &m.DepthCM
	case "weight_kg":
		return &m.WeightKG
	case "volume_l":
		return &m.VolumeL
	case "volume_expanded_l":
		return &m.VolumeExpandedL
	case "handle_drop_cm":
		return &m.HandleDropCM
	case "laptop_max_inch":
		return &m.LaptopMaxInch
	case "heel_height_cm":
		return &m.HeelHeightCM
	case "shaft_height_cm":
		return &m.ShaftHeightCM
	case "inseam_cm":
		return &m.InseamCM
	}
	return nil
}

// IsEmpty reports whether no measurement is stated (a node stores that as null).
func (m *Measurements) IsEmpty() bool {
	if m == nil {
		return true
	}
	for _, k := range MeasurementKeys {
		if k != "strap_length_cm" && m.Number(k) != nil {
			return false
		}
	}
	return m.StrapLengthCM == nil
}

// Validate lists the measurements that are not positive numbers (and a
// strap whose min is larger than its max), as "key: reason".
func (m *Measurements) Validate() []string {
	if m == nil {
		return nil
	}
	var out []string
	bad := func(v float64) bool { return !(v > 0) || math.IsInf(v, 0) }
	for _, k := range MeasurementKeys {
		if k == "strap_length_cm" {
			if r := m.StrapLengthCM; r != nil {
				switch {
				case bad(r.Min) || bad(r.Max):
					out = append(out, k+": min and max must be positive numbers")
				case r.Min > r.Max:
					out = append(out, k+": min is larger than max")
				}
			}
			continue
		}
		if v := m.Number(k); v != nil && bad(*v) {
			out = append(out, k+": must be a positive number")
		}
	}
	return out
}

// Feature registry keys (docs/protocol.md 5.9.4), in registry order.
var FeatureKeys = []string{"shell", "wheels", "lock", "expandable", "closure", "laptop_compartment", "water_resistance", "detachable_parts", "adjustable_parts", "fit", "sleeve_length", "neckline"}

// FeatureType is the type of a registry feature.
type FeatureType string

// Feature types.
const (
	FeatureEnum    FeatureType = "enum"
	FeatureInteger FeatureType = "integer"
	FeatureBoolean FeatureType = "boolean"
	FeatureList    FeatureType = "list"
)

// FeatureSpec describes one registry key: its type and allowed values (for
// integer features the allowed numbers as text).
type FeatureSpec struct {
	Type   FeatureType
	Values []string
}

// FeatureRegistry is the typed feature registry of docs/protocol.md 5.9.4.
var FeatureRegistry = map[string]FeatureSpec{
	"shell":              {FeatureEnum, []string{"hard", "soft", "hybrid"}},
	"wheels":             {FeatureInteger, []string{"0", "2", "4", "8"}},
	"lock":               {FeatureEnum, []string{"none", "combination", "tsa_combination", "tsa_key", "key"}},
	"expandable":         {FeatureBoolean, nil},
	"closure":            {FeatureEnum, []string{"zip", "magnet", "flap", "buckle", "drawstring", "button", "hook_and_loop", "laces", "slip_on", "open"}},
	"laptop_compartment": {FeatureBoolean, nil},
	"water_resistance":   {FeatureEnum, []string{"none", "water_repellent", "waterproof"}},
	"detachable_parts":   {FeatureList, []string{"shoulder_strap", "handle", "lining", "insole", "hood", "belt", "pouch", "wheels"}},
	"adjustable_parts":   {FeatureList, []string{"shoulder_strap", "waist", "hem", "cuff"}},
	"fit":                {FeatureEnum, []string{"slim", "regular", "relaxed", "oversized"}},
	"sleeve_length":      {FeatureEnum, []string{"sleeveless", "short", "three_quarter", "long"}},
	"neckline":           {FeatureEnum, []string{"crew", "v_neck", "collar", "hood", "turtleneck", "boat", "scoop"}},
}

var featureKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Features are the typed product features (schemas/Features.json). Registry
// keys have typed fields (nil = not stated); every other key is kept in
// Other with its JSON value (string, float64, bool or []any), so a document
// round-trips without losing what this package does not know. A registry key
// whose value does not have the registry type is also kept in Other, so
// decoding never fails on content; Validate reports it.
type Features struct {
	Shell             *string
	Wheels            *int
	Lock              *string
	Expandable        *bool
	Closure           *string
	LaptopCompartment *bool
	WaterResistance   *string
	DetachableParts   []string
	AdjustableParts   []string
	Fit               *string
	SleeveLength      *string
	Neckline          *string
	// Other holds the keys outside the registry (and registry keys with a
	// value of the wrong type).
	Other map[string]any
}

// Get returns the value of a feature as it appears in JSON (string, int,
// bool, []string or for other keys the decoded value), and whether it is stated.
func (f *Features) Get(key string) (any, bool) {
	if f == nil {
		return nil, false
	}
	if v, ok := f.getTyped(key); ok {
		return v, true
	}
	v, ok := f.Other[key]
	return v, ok
}

func (f *Features) getTyped(key string) (any, bool) {
	switch key {
	case "shell":
		return deref(f.Shell)
	case "wheels":
		return deref(f.Wheels)
	case "lock":
		return deref(f.Lock)
	case "expandable":
		return deref(f.Expandable)
	case "closure":
		return deref(f.Closure)
	case "laptop_compartment":
		return deref(f.LaptopCompartment)
	case "water_resistance":
		return deref(f.WaterResistance)
	case "detachable_parts":
		return f.DetachableParts, f.DetachableParts != nil
	case "adjustable_parts":
		return f.AdjustableParts, f.AdjustableParts != nil
	case "fit":
		return deref(f.Fit)
	case "sleeve_length":
		return deref(f.SleeveLength)
	case "neckline":
		return deref(f.Neckline)
	}
	return nil, false
}

func deref[T any](p *T) (any, bool) {
	if p == nil {
		return nil, false
	}
	return *p, true
}

// Keys returns the stated keys: registry keys in registry order, then the
// other keys sorted.
func (f *Features) Keys() []string {
	if f == nil {
		return nil
	}
	var out []string
	for _, k := range FeatureKeys {
		if _, ok := f.typed(k); ok {
			out = append(out, k)
		}
	}
	var other []string
	for k := range f.Other {
		if _, typed := f.typed(k); !typed {
			other = append(other, k)
		}
	}
	sort.Strings(other)
	return append(out, other...)
}

func (f *Features) typed(key string) (any, bool) { return f.getTyped(key) }

// IsEmpty reports whether no feature is stated (a node stores that as null).
func (f *Features) IsEmpty() bool { return f == nil || len(f.Keys()) == 0 }

// Set stores a value under key: typed when key is a registry key and the
// value has its type (string for enums, an integer for wheels, bool, a
// []string or []any of strings for lists), otherwise in Other. A nil value
// removes the key.
func (f *Features) Set(key string, v any) {
	if f.Other != nil {
		delete(f.Other, key)
	}
	f.clearTyped(key)
	if v == nil {
		return
	}
	if !f.setTyped(key, v) {
		if f.Other == nil {
			f.Other = map[string]any{}
		}
		f.Other[key] = v
	}
}

func (f *Features) clearTyped(key string) {
	switch key {
	case "shell":
		f.Shell = nil
	case "wheels":
		f.Wheels = nil
	case "lock":
		f.Lock = nil
	case "expandable":
		f.Expandable = nil
	case "closure":
		f.Closure = nil
	case "laptop_compartment":
		f.LaptopCompartment = nil
	case "water_resistance":
		f.WaterResistance = nil
	case "detachable_parts":
		f.DetachableParts = nil
	case "adjustable_parts":
		f.AdjustableParts = nil
	case "fit":
		f.Fit = nil
	case "sleeve_length":
		f.SleeveLength = nil
	case "neckline":
		f.Neckline = nil
	}
}

func (f *Features) setTyped(key string, v any) bool {
	spec, ok := FeatureRegistry[key]
	if !ok {
		return false
	}
	switch spec.Type {
	case FeatureEnum:
		s, ok := v.(string)
		if !ok {
			return false
		}
		switch key {
		case "shell":
			f.Shell = &s
		case "lock":
			f.Lock = &s
		case "closure":
			f.Closure = &s
		case "water_resistance":
			f.WaterResistance = &s
		case "fit":
			f.Fit = &s
		case "sleeve_length":
			f.SleeveLength = &s
		case "neckline":
			f.Neckline = &s
		}
	case FeatureInteger:
		var n int
		switch x := v.(type) {
		case int:
			n = x
		case float64:
			if x != math.Trunc(x) || math.Abs(x) > 1e9 {
				return false
			}
			n = int(x)
		case json.Number:
			fl, err := x.Float64()
			if err != nil || fl != math.Trunc(fl) || math.Abs(fl) > 1e9 {
				return false
			}
			n = int(fl)
		default:
			return false
		}
		f.Wheels = &n
	case FeatureBoolean:
		b, ok := v.(bool)
		if !ok {
			return false
		}
		if key == "expandable" {
			f.Expandable = &b
		} else {
			f.LaptopCompartment = &b
		}
	case FeatureList:
		var list []string
		switch x := v.(type) {
		case []string:
			list = append([]string{}, x...)
		case []any:
			list = make([]string, 0, len(x))
			for _, e := range x {
				s, ok := e.(string)
				if !ok {
					return false
				}
				list = append(list, s)
			}
		default:
			return false
		}
		if key == "detachable_parts" {
			f.DetachableParts = list
		} else {
			f.AdjustableParts = list
		}
	}
	return true
}

// MarshalJSON writes the stated features: registry keys in registry order,
// then the other keys sorted.
func (f Features) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range f.Keys() {
		v, _ := f.Get(k)
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("feature %s: %w", k, err)
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads a features object. It fails only on JSON that is not
// an object; values of the wrong type are kept in Other (see Validate).
func (f *Features) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*f = Features{}
	for k, rv := range raw {
		dec := json.NewDecoder(bytes.NewReader(rv))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if v == nil {
			continue // null: not stated
		}
		if n, ok := v.(json.Number); ok {
			if _, reg := FeatureRegistry[k]; !reg || FeatureRegistry[k].Type != FeatureInteger {
				fl, err := n.Float64()
				if err != nil {
					return err
				}
				v = fl
			}
		}
		if list, ok := v.([]any); ok {
			for i, e := range list {
				if n, ok := e.(json.Number); ok {
					fl, err := n.Float64()
					if err != nil {
						return err
					}
					list[i] = fl
				}
			}
		}
		f.Set(k, v)
		if n, ok := f.Other[k].(json.Number); ok {
			fl, _ := n.Float64()
			f.Other[k] = fl
		}
	}
	return nil
}

// Validate checks the registry keys against their type and values and every
// other key against the key pattern; it returns "key: reason" per problem.
// A node answers a write with such a problem with 422 validation_failed.
func (f *Features) Validate() []string {
	if f == nil {
		return nil
	}
	var out []string
	for _, k := range f.Keys() {
		v, _ := f.Get(k)
		if !featureKeyPattern.MatchString(k) {
			out = append(out, k+": not a valid feature key")
			continue
		}
		spec, reg := FeatureRegistry[k]
		if !reg {
			if !validOtherFeature(v) {
				out = append(out, k+": the value must be a string, number, boolean or a list of those")
			}
			continue
		}
		if _, other := f.Other[k]; other {
			out = append(out, fmt.Sprintf("%s: must be %s", k, describeFeature(spec)))
			continue
		}
		switch spec.Type {
		case FeatureEnum:
			if s := v.(string); !Contains(spec.Values, s) {
				out = append(out, fmt.Sprintf("%s: %q is not one of %s", k, s, strings.Join(spec.Values, ", ")))
			}
		case FeatureInteger:
			if n := fmt.Sprint(v); !Contains(spec.Values, n) {
				out = append(out, fmt.Sprintf("%s: %s is not one of %s", k, n, strings.Join(spec.Values, ", ")))
			}
		case FeatureList:
			seen := map[string]bool{}
			for _, s := range v.([]string) {
				if !Contains(spec.Values, s) {
					out = append(out, fmt.Sprintf("%s: %q is not one of %s", k, s, strings.Join(spec.Values, ", ")))
				} else if seen[s] {
					out = append(out, fmt.Sprintf("%s: %q appears twice", k, s))
				}
				seen[s] = true
			}
		}
	}
	return out
}

func describeFeature(spec FeatureSpec) string {
	switch spec.Type {
	case FeatureBoolean:
		return "true or false"
	case FeatureList:
		return "a list of " + strings.Join(spec.Values, ", ")
	}
	return "one of " + strings.Join(spec.Values, ", ")
}

func validOtherFeature(v any) bool {
	scalar := func(x any) bool {
		switch x.(type) {
		case string, float64, int, bool, json.Number:
			return true
		}
		return false
	}
	if scalar(v) {
		return true
	}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if !scalar(e) {
				return false
			}
		}
		return true
	case []string:
		return true
	}
	return false
}

// Label completeness warning codes (schemas/Common.json WarningCode,
// docs/import-format.md rule 10). They never block a write or publishing.
// Clients must ignore unknown codes.
const (
	WarnFibreCompositionMissing  = "fibre_composition_missing"
	WarnAnimalOriginUnknown      = "animal_origin_unknown"
	WarnFootwearMaterialsMissing = "footwear_materials_missing"
	WarnFibrePercentagesNot100   = "fibre_percentages_not_100"
)

// WarningCodes lists the label completeness warning codes.
var WarningCodes = []string{WarnFibreCompositionMissing, WarnAnimalOriginUnknown, WarnFootwearMaterialsMissing, WarnFibrePercentagesNot100}

// TextileItemTypes are the item types that get the textile label warnings:
// the recommended apparel keys (t_shirt to socks, hat, cap, scarf, gloves,
// tie), bag, backpack and luggage (docs/import-format.md rule 10).
var TextileItemTypes = []string{"t_shirt", "top", "shirt", "blouse", "polo", "sweater", "cardigan", "hoodie", "sweatshirt", "vest", "jacket", "coat", "blazer", "suit", "dress", "skirt", "trousers", "jeans", "shorts", "jumpsuit", "leggings", "underwear", "lingerie", "nightwear", "swimwear", "socks", "hat", "cap", "scarf", "gloves", "tie", "bag", "backpack", "luggage"}

// FootwearItemTypes are the item types that get the footwear label warning.
var FootwearItemTypes = []string{"shoes", "sneakers", "boots", "sandals"}

// AnimalOriginFibres are the fibre keys that make animal_origin_unknown
// apply; every key ending in _wool counts as a kind of wool too.
var AnimalOriginFibres = []string{"leather", "suede", "fur", "down", "feather", "wool", "merino_wool", "lambswool", "cashmere", "alpaca", "mohair", "silk"}

// IsAnimalOriginFibre reports whether a (normalised) fibre key is of animal
// origin for the animal_origin_unknown warning.
func IsAnimalOriginFibre(key string) bool {
	return Contains(AnimalOriginFibres, key) || strings.HasSuffix(key, "_wool")
}

// LabelWarning is one label completeness warning of a product.
type LabelWarning struct {
	Code string
	// Column is the products.csv column the warning names.
	Column string
	// Pointer is the JSON Pointer into the product write model.
	Pointer string
	Detail  string
}

// WarningDetail returns the warning as an IngestResult warning_details entry.
func (w LabelWarning) WarningDetail() WarningDetail {
	p := w.Pointer
	return WarningDetail{Code: w.Code, Pointer: &p, Detail: w.Detail}
}

// LabelWarnings checks a product as stored (docs/import-format.md rule 10)
// and returns what a consumer label would miss: fibre_composition_missing,
// animal_origin_unknown, footwear_materials_missing and (once per part)
// fibre_percentages_not_100, in that order. Textile item types get the
// three textile warnings, footwear item types the footwear warning; other
// and unknown item types get none.
func LabelWarnings(itemType *string, materials Materials, containsAnimalOrigin *bool, footwear *FootwearMaterials) []LabelWarning {
	it := Str(itemType)
	var out []LabelWarning
	switch {
	case Contains(TextileItemTypes, it):
		if len(materials) == 0 {
			out = append(out, LabelWarning{WarnFibreCompositionMissing, "materials", "/materials",
				"the fibre composition is missing; it must be on the label of a textile product and shown before purchase (Regulation (EU) No 1007/2011)"})
		}
		if containsAnimalOrigin == nil {
		animal:
			for _, p := range materials {
				for _, f := range p.Fibers {
					if IsAnimalOriginFibre(NormalizeFiber(f).Fiber) {
						out = append(out, LabelWarning{WarnAnimalOriginUnknown, "contains_animal_origin", "/contains_animal_origin",
							fmt.Sprintf("the materials contain %s; state whether the product contains non-textile parts of animal origin (Regulation (EU) No 1007/2011 Art. 12)", f.Fiber)})
						break animal
					}
				}
			}
		}
		for i, p := range materials {
			sum := 0.0
			for _, f := range p.Fibers {
				sum += f.Percentage
			}
			if math.Abs(sum-100) > 0.01 {
				out = append(out, LabelWarning{WarnFibrePercentagesNot100, "materials", fmt.Sprintf("/materials/%d", i),
					fmt.Sprintf("the percentages of %s add up to %g, not 100", p.Part, sum)})
			}
		}
	case Contains(FootwearItemTypes, it):
		if !footwear.Complete() {
			var missing []string
			for _, k := range FootwearParts {
				if footwear.Part(k) == nil {
					missing = append(missing, k)
				}
			}
			out = append(out, LabelWarning{WarnFootwearMaterialsMissing, "footwear_materials", "/footwear_materials",
				"the footwear materials of " + strings.Join(missing, ", ") + " are missing; they must be on the label of footwear (Directive 94/11/EC)"})
		}
	}
	return out
}
