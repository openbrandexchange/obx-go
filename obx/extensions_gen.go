package obx

// Code below keeps extension members (see extensions.go) on every type
// whose schema allows them.

import "encoding/json"

var _ = json.Marshal

// MarshalJSON writes Brand with its extension members.
func (v Brand) MarshalJSON() ([]byte, error) {
	type plain Brand
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Brand and keeps its extension members in X.
func (v *Brand) UnmarshalJSON(data []byte) error {
	type plain Brand
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Brand(p)
	v.X = x
	return nil
}

// MarshalJSON writes Term with its extension members.
func (v Term) MarshalJSON() ([]byte, error) {
	type plain Term
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Term and keeps its extension members in X.
func (v *Term) UnmarshalJSON(data []byte) error {
	type plain Term
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Term(p)
	v.X = x
	return nil
}

// MarshalJSON writes MaterialPart with its extension members.
func (v MaterialPart) MarshalJSON() ([]byte, error) {
	type plain MaterialPart
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads MaterialPart and keeps its extension members in X.
func (v *MaterialPart) UnmarshalJSON(data []byte) error {
	type plain MaterialPart
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = MaterialPart(p)
	v.X = x
	return nil
}

// MarshalJSON writes CareInstruction with its extension members.
func (v CareInstruction) MarshalJSON() ([]byte, error) {
	type plain CareInstruction
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads CareInstruction and keeps its extension members in X.
func (v *CareInstruction) UnmarshalJSON(data []byte) error {
	type plain CareInstruction
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = CareInstruction(p)
	v.X = x
	return nil
}

// MarshalJSON writes Product with its extension members.
func (v Product) MarshalJSON() ([]byte, error) {
	type plain Product
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Product and keeps its extension members in X.
func (v *Product) UnmarshalJSON(data []byte) error {
	type plain Product
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Product(p)
	v.X = x
	return nil
}

// MarshalJSON writes Color with its extension members.
func (v Color) MarshalJSON() ([]byte, error) {
	type plain Color
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Color and keeps its extension members in X.
func (v *Color) UnmarshalJSON(data []byte) error {
	type plain Color
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Color(p)
	v.X = x
	return nil
}

// MarshalJSON writes Variant with its extension members.
func (v Variant) MarshalJSON() ([]byte, error) {
	type plain Variant
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Variant and keeps its extension members in X.
func (v *Variant) UnmarshalJSON(data []byte) error {
	type plain Variant
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Variant(p)
	v.X = x
	return nil
}

// MarshalJSON writes Rights with its extension members.
func (v Rights) MarshalJSON() ([]byte, error) {
	type plain Rights
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Rights and keeps its extension members in X.
func (v *Rights) UnmarshalJSON(data []byte) error {
	type plain Rights
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Rights(p)
	v.X = x
	return nil
}

// MarshalJSON writes Provenance with its extension members.
func (v Provenance) MarshalJSON() ([]byte, error) {
	type plain Provenance
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Provenance and keeps its extension members in X.
func (v *Provenance) UnmarshalJSON(data []byte) error {
	type plain Provenance
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Provenance(p)
	v.X = x
	return nil
}

// MarshalJSON writes Asset with its extension members.
func (v Asset) MarshalJSON() ([]byte, error) {
	type plain Asset
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Asset and keeps its extension members in X.
func (v *Asset) UnmarshalJSON(data []byte) error {
	type plain Asset
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Asset(p)
	v.X = x
	return nil
}

// MarshalJSON writes BrandInput with its extension members.
func (v BrandInput) MarshalJSON() ([]byte, error) {
	type plain BrandInput
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads BrandInput and keeps its extension members in X.
func (v *BrandInput) UnmarshalJSON(data []byte) error {
	type plain BrandInput
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = BrandInput(p)
	v.X = x
	return nil
}

// MarshalJSON writes ProductInput with its extension members.
func (v ProductInput) MarshalJSON() ([]byte, error) {
	type plain ProductInput
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads ProductInput and keeps its extension members in X.
func (v *ProductInput) UnmarshalJSON(data []byte) error {
	type plain ProductInput
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = ProductInput(p)
	v.X = x
	return nil
}

// MarshalJSON writes VariantInput with its extension members.
func (v VariantInput) MarshalJSON() ([]byte, error) {
	type plain VariantInput
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads VariantInput and keeps its extension members in X.
func (v *VariantInput) UnmarshalJSON(data []byte) error {
	type plain VariantInput
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = VariantInput(p)
	v.X = x
	return nil
}

// MarshalJSON writes RightsInput with its extension members.
func (v RightsInput) MarshalJSON() ([]byte, error) {
	type plain RightsInput
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads RightsInput and keeps its extension members in X.
func (v *RightsInput) UnmarshalJSON(data []byte) error {
	type plain RightsInput
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = RightsInput(p)
	v.X = x
	return nil
}

// MarshalJSON writes ProvenanceInput with its extension members.
func (v ProvenanceInput) MarshalJSON() ([]byte, error) {
	type plain ProvenanceInput
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads ProvenanceInput and keeps its extension members in X.
func (v *ProvenanceInput) UnmarshalJSON(data []byte) error {
	type plain ProvenanceInput
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = ProvenanceInput(p)
	v.X = x
	return nil
}

// MarshalJSON writes AssetInput with its extension members.
func (v AssetInput) MarshalJSON() ([]byte, error) {
	type plain AssetInput
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads AssetInput and keeps its extension members in X.
func (v *AssetInput) UnmarshalJSON(data []byte) error {
	type plain AssetInput
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = AssetInput(p)
	v.X = x
	return nil
}

// MarshalJSON writes FootwearMaterials with its extension members.
func (v FootwearMaterials) MarshalJSON() ([]byte, error) {
	type plain FootwearMaterials
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads FootwearMaterials and keeps its extension members in X.
func (v *FootwearMaterials) UnmarshalJSON(data []byte) error {
	type plain FootwearMaterials
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = FootwearMaterials(p)
	v.X = x
	return nil
}

// MarshalJSON writes Certification with its extension members.
func (v Certification) MarshalJSON() ([]byte, error) {
	type plain Certification
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Certification and keeps its extension members in X.
func (v *Certification) UnmarshalJSON(data []byte) error {
	type plain Certification
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Certification(p)
	v.X = x
	return nil
}

// MarshalJSON writes Measurements with its extension members.
func (v Measurements) MarshalJSON() ([]byte, error) {
	type plain Measurements
	return marshalExt(plain(v), v.X)
}

// UnmarshalJSON reads Measurements and keeps its extension members in X.
func (v *Measurements) UnmarshalJSON(data []byte) error {
	type plain Measurements
	var p plain
	x, err := unmarshalExt(data, &p)
	if err != nil {
		return err
	}
	*v = Measurements(p)
	v.X = x
	return nil
}
