package canonicalcsv

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/openbrandexchange/obx-go/obx"
)

// Image is an image file of an import: uploaded with it or listed in the
// images_manifest.
type Image struct {
	Name        string // base file name
	SHA256      string
	Bytes       int64
	ContentType string
	Width       int
	Height      int
	// Renditions are client-generated renditions from an images manifest.
	Renditions *obx.ManifestRenditions
}

// CatalogBrand is a brand that already exists on the node.
type CatalogBrand struct {
	ExternalID string
	Name       string
}

// CatalogProduct is a product that already exists on the node.
type CatalogProduct struct {
	ExternalID  string
	StyleNumber string
	BrandID     string   // brand external id
	Colors      []string // colour codes of its variants
}

// Catalog is what the planner needs to know about the node.
type Catalog interface {
	Brands() []CatalogBrand
	// FindProduct looks a product up by external id, else by style number
	// (case-insensitive).
	FindProduct(styleOrID string) (CatalogProduct, bool)
}

// PlannedBrand is a brand the import creates because products.csv names it.
type PlannedBrand struct {
	ExternalID string
	Name       string
}

// PlannedAsset is one image to write.
type PlannedAsset struct {
	ExternalID string
	Image      Image
	AssetsRow  int // row in assets.csv, 0 when none
	Input      obx.AssetInput
}

// PlannedProduct groups the writes of one product: the product itself (nil
// Input when only images arrive for an existing product), its variants and
// its assets.
type PlannedProduct struct {
	ExternalID      string
	StyleNumber     string
	BrandExternalID string
	Row             int
	Input           *obx.ProductInput
	Variants        []Variant
	Assets          []PlannedAsset
}

// Plan is the ordered result of an import: apply brands, then per product
// the product, its variants, then its assets.
type Plan struct {
	Brands   []PlannedBrand
	Products []*PlannedProduct
	Problems []obx.ImportRowError
	// MirrorBrands are the external ids of the brands present in products.csv
	// (the scope of a mirror import).
	MirrorBrands []string
	// Rows is the number of data rows read; Files the number of images.
	Rows, Files int
}

// Errors counts problems with severity error.
func (p *Plan) Errors() int { return countSeverity(p.Problems, obx.SeverityError) }

// Warnings counts problems with severity warning.
func (p *Plan) Warnings() int { return countSeverity(p.Problems, obx.SeverityWarning) }

// BuildPlan resolves brands, products and images against the catalog. Any of
// products, assets and images may be nil/empty.
func BuildPlan(products *ProductsFile, assets *AssetsFile, images []Image, cat Catalog) *Plan {
	plan := &Plan{Files: len(images)}
	if products != nil {
		plan.Rows = products.Rows
		plan.Problems = append(plan.Problems, products.Problems...)
	}
	if assets != nil {
		plan.Problems = append(plan.Problems, assets.Problems...)
	}

	// Brands.
	existing := cat.Brands()
	brandByKey := map[string]string{} // lowercase name or external id → external id
	for _, b := range existing {
		brandByKey[strings.ToLower(b.ExternalID)] = b.ExternalID
		brandByKey[strings.ToLower(b.Name)] = b.ExternalID
	}
	knownBrands := len(existing)
	resolveBrand := func(p *Product) (string, bool) {
		if p.Brand == "" {
			if knownBrands == 1 && len(plan.Brands) == 0 {
				return existing[0].ExternalID, true
			}
			if knownBrands == 0 && len(plan.Brands) == 1 {
				return plan.Brands[0].ExternalID, true
			}
			return "", false
		}
		if id, ok := brandByKey[strings.ToLower(p.Brand)]; ok {
			return id, true
		}
		id := obx.Slug(p.Brand)
		if id == "" {
			id = "brand"
		}
		if prev, ok := brandByKey[id]; ok {
			return prev, true
		}
		plan.Brands = append(plan.Brands, PlannedBrand{ExternalID: id, Name: p.Brand})
		brandByKey[strings.ToLower(p.Brand)] = id
		brandByKey[id] = id
		return id, true
	}

	byID := map[string]*PlannedProduct{}
	byStyle := map[string]*PlannedProduct{}
	mirror := map[string]bool{}
	file := "products.csv"
	if products != nil {
		file = products.File
		for _, p := range products.Products {
			brandID, ok := resolveBrand(p)
			if !ok {
				plan.Problems = append(plan.Problems, problem(file, p.Row, "brand", obx.ErrValidationFailed, obx.SeverityError,
					fmt.Sprintf("product %s needs a brand: this catalog has more than one brand", p.ExternalID), ""))
				continue
			}
			in := p.Input
			b := brandID
			in.BrandExternalID = &b
			pp := &PlannedProduct{ExternalID: p.ExternalID, StyleNumber: in.StyleNumber, BrandExternalID: brandID, Row: p.Row, Input: &in, Variants: p.Variants}
			plan.Products = append(plan.Products, pp)
			byID[p.ExternalID] = pp
			byStyle[strings.ToLower(in.StyleNumber)] = pp
			if !mirror[brandID] {
				mirror[brandID] = true
				plan.MirrorBrands = append(plan.MirrorBrands, brandID)
			}
		}
	}

	// Images.
	rowsByFile := map[string]*AssetRow{}
	if assets != nil {
		for _, r := range assets.Rows {
			rowsByFile[strings.ToLower(r.File)] = r
		}
	}
	provided := map[string]bool{}
	type resolved struct {
		asset   PlannedAsset
		product *PlannedProduct
	}
	var list []resolved
	idCount := map[string]int{}
	extra := map[string]*PlannedProduct{} // catalog products that only get images
	sortedImages := append([]Image(nil), images...)
	sort.SliceStable(sortedImages, func(i, j int) bool { return sortedImages[i].Name < sortedImages[j].Name })
	for _, img := range sortedImages {
		img.Name = path.Base(strings.ReplaceAll(img.Name, "\\", "/"))
		provided[strings.ToLower(img.Name)] = true
		row := rowsByFile[strings.ToLower(img.Name)]
		imgErr := func(code, detail string) {
			if row != nil {
				plan.Problems = append(plan.Problems, problem(assets.File, row.Row, "file", code, obx.SeverityError, detail, img.Name))
			} else {
				plan.Problems = append(plan.Problems, problem(img.Name, noRow, "", code, obx.SeverityError, detail, ""))
			}
		}
		parsed, perr := ParseImageName(img.Name)
		style, color, pos := parsed.Style, parsed.Color, parsed.Position
		colorSet := perr == nil
		if row != nil {
			if row.ProductID != nil {
				style = *row.ProductID
			}
			if row.ColorCode != nil {
				color, colorSet = *row.ColorCode, true
			}
			if row.Position != nil {
				pos = *row.Position
			}
		}
		if style == "" || !colorSet || pos == 0 {
			if perr != nil {
				imgErr(obx.ErrValidationFailed, perr.Error())
			} else {
				imgErr(obx.ErrValidationFailed, "cannot tell style, colour and position of "+img.Name)
			}
			continue
		}
		// Product: this import first, then the catalog.
		var pp *PlannedProduct
		var colors []string
		if p, ok := byID[style]; ok {
			pp = p
		} else if p, ok := byStyle[strings.ToLower(style)]; ok {
			pp = p
		}
		if cp, ok := cat.FindProduct(style); ok {
			if pp == nil {
				if p, ok := extra[cp.ExternalID]; ok {
					pp = p
				} else {
					pp = &PlannedProduct{ExternalID: cp.ExternalID, StyleNumber: cp.StyleNumber, BrandExternalID: cp.BrandID}
					extra[cp.ExternalID] = pp
				}
			}
			if cp.ExternalID == pp.ExternalID {
				colors = append(colors, cp.Colors...)
			}
		}
		if pp == nil {
			imgErr(obx.ErrUnknownParent, fmt.Sprintf("style %s is not in the catalog", style))
			continue
		}
		for _, v := range pp.Variants {
			colors = append(colors, v.Input.Color.Code)
		}
		if color != "" {
			match := ""
			for _, c := range colors {
				if strings.EqualFold(c, color) {
					match = c
					break
				}
			}
			if match == "" {
				imgErr(obx.ErrUnknownParent, fmt.Sprintf("colour %s is not a colour of %s", color, pp.StyleNumber))
				continue
			}
			color = match
		}
		extID := AssetExternalID(pp.StyleNumber, color, pos)
		if row != nil && row.AssetID != nil {
			extID = *row.AssetID
		}
		in := obx.AssetInput{ProductExternalID: pp.ExternalID, MediaType: "image", Role: obx.RolePackshot, Position: pos}
		if color != "" {
			c := color
			in.ColorCode = &c
		}
		if perr == nil {
			in.Role = parsed.Role
			in.View = parsed.View
		}
		name := img.Name
		in.Filename = &name
		if row != nil {
			if row.Role != nil {
				in.Role = *row.Role
			}
			if row.View != nil {
				in.View = row.View
			}
			in.AltText = row.AltText
			in.Rights = row.Rights
			in.Provenance = row.Provenance
			in.Status = row.Status
		}
		files := &obx.AssetFiles{Original: &obx.FileRef{SHA256: img.SHA256}}
		if img.Width > 0 && img.Height > 0 {
			w, h := img.Width, img.Height
			files.Original.Width, files.Original.Height = &w, &h
		}
		if r := img.Renditions; r != nil {
			for name, ref := range map[string]*obx.BlobRef{obx.RenditionWeb: r.Web, obx.RenditionSquare: r.Square, obx.RenditionThumb: r.Thumb} {
				if ref != nil {
					w, h := ref.Width, ref.Height
					files.Set(name, &obx.FileRef{SHA256: ref.SHA256, Width: &w, Height: &h})
				}
			}
		}
		in.Files = files
		ar := 0
		if row != nil {
			ar = row.Row
		}
		idCount[extID]++
		list = append(list, resolved{asset: PlannedAsset{ExternalID: extID, Image: img, AssetsRow: ar, Input: in}, product: pp})
	}
	for _, r := range list {
		if idCount[r.asset.ExternalID] > 1 {
			plan.Problems = append(plan.Problems, problem(r.asset.Image.Name, noRow, "", obx.ErrValidationFailed, obx.SeverityError,
				fmt.Sprintf("another file in this import is also image %s (same style, colour and position)", r.asset.ExternalID), ""))
			continue
		}
		r.product.Assets = append(r.product.Assets, r.asset)
	}
	if assets != nil {
		for _, r := range assets.Rows {
			if !provided[strings.ToLower(r.File)] {
				plan.Problems = append(plan.Problems, problem(assets.File, r.Row, "file", obx.ErrFilesMissing, obx.SeverityError,
					fmt.Sprintf("%s is listed but was not uploaded", r.File), r.File))
			}
		}
	}
	extraIDs := make([]string, 0, len(extra))
	for id := range extra {
		extraIDs = append(extraIDs, id)
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		if len(extra[id].Assets) > 0 {
			plan.Products = append(plan.Products, extra[id])
		}
	}
	for _, pp := range plan.Products {
		sort.SliceStable(pp.Assets, func(i, j int) bool {
			a, b := pp.Assets[i].Input, pp.Assets[j].Input
			ac, bc := obx.Str(a.ColorCode), obx.Str(b.ColorCode)
			if ac != bc {
				return ac < bc
			}
			return a.Position < b.Position
		})
	}
	return plan
}
