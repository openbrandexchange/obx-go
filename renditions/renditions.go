// Package renditions produces the derived OBX renditions of an image in pure
// Go (docs/protocol.md section 5.5), so the obx CLI and the mock node can
// supply web, square and thumb files where a node has no image library:
//
//   - web: JPEG, longest side at most 2000 px, never upscaled, transparency
//     flattened on white;
//   - square: the web image centred on a white square whose side is the
//     longest side of web;
//   - thumb: JPEG, longest side at most 600 px, never upscaled.
//
// The original is never touched. Colour management is not applied: pixels
// are taken as sRGB (embedded ICC profiles are ignored).
package renditions

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register PNG
	"io"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff" // register TIFF
	_ "golang.org/x/image/webp" // register WebP

	"github.com/openbrandexchange/obx-go/obx"
)

// Rendition limits.
const (
	WebMaxSide   = 2000
	ThumbMaxSide = 600
	// MaxPixels guards against decompression bombs (100 megapixels).
	MaxPixels = 100_000_000
)

// JPEG qualities used for the renditions.
const (
	WebQuality   = 86
	ThumbQuality = 82
)

// ErrTooLarge is returned for images above MaxPixels.
var ErrTooLarge = errors.New("renditions: image has more than 100 megapixels")

// ErrUnsupported is returned for data that is not a JPEG, PNG, WebP or TIFF.
var ErrUnsupported = errors.New("renditions: not a JPEG, PNG, WebP or TIFF image")

// File is one produced rendition.
type File struct {
	Name        string // web, square or thumb
	Data        []byte
	ContentType string // always image/jpeg
	Width       int
	Height      int
	SHA256      string
}

// Ref returns the reference used in AssetInput.files and images manifests.
func (f *File) Ref() *obx.BlobRef {
	return &obx.BlobRef{SHA256: f.SHA256, Width: f.Width, Height: f.Height}
}

// Set holds the three derived renditions.
type Set struct {
	Web, Square, Thumb File
}

// All returns the renditions in canonical order.
func (s *Set) All() []*File { return []*File{&s.Web, &s.Square, &s.Thumb} }

// Manifest returns the renditions as an images-manifest entry part.
func (s *Set) Manifest() *obx.ManifestRenditions {
	return &obx.ManifestRenditions{Web: s.Web.Ref(), Square: s.Square.Ref(), Thumb: s.Thumb.Ref()}
}

// Info describes an image from its header.
type Info struct {
	ContentType string
	Width       int
	Height      int
}

// DetectContentType recognises JPEG, PNG, WebP and TIFF by their magic bytes
// and returns "" for anything else (SVG, GIF, HTML …).
func DetectContentType(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*"):
		return "image/tiff"
	}
	return ""
}

// ReadInfo reads the content type and dimensions from the header without
// decoding the pixels.
func ReadInfo(data []byte) (Info, error) {
	ct := DetectContentType(data)
	if ct == "" {
		return Info{}, ErrUnsupported
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Info{ContentType: ct}, fmt.Errorf("renditions: read %s header: %w", ct, err)
	}
	return Info{ContentType: ct, Width: cfg.Width, Height: cfg.Height}, nil
}

// ReadInfoFrom reads only as much of r as needed for the header.
func ReadInfoFrom(r io.Reader) (Info, error) {
	head := make([]byte, 64<<10)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Info{}, err
	}
	return ReadInfo(head[:n])
}

// Fit returns the size of a w×h image scaled so its longest side is at most
// max, never upscaled.
func Fit(w, h, max int) (int, int) {
	if w <= max && h <= max {
		return w, h
	}
	if w >= h {
		nh := int(float64(h)*float64(max)/float64(w) + 0.5)
		return max, clamp1(nh)
	}
	nw := int(float64(w)*float64(max)/float64(h) + 0.5)
	return clamp1(nw), max
}

func clamp1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// Generate decodes original and produces web, square and thumb.
func Generate(original []byte) (*Set, error) {
	info, err := ReadInfo(original)
	if err != nil {
		return nil, err
	}
	if int64(info.Width)*int64(info.Height) > MaxPixels {
		return nil, ErrTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		return nil, fmt.Errorf("renditions: decode %s: %w", info.ContentType, err)
	}
	src = applyOrientation(src, jpegOrientation(original))
	b := src.Bounds()
	ww, wh := Fit(b.Dx(), b.Dy(), WebMaxSide)
	web := scale(flatten(src), ww, wh)
	set := &Set{}
	if err := encode(&set.Web, "web", web, WebQuality); err != nil {
		return nil, err
	}
	side := ww
	if wh > side {
		side = wh
	}
	sq := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(sq, sq.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	off := image.Pt((side-ww)/2, (side-wh)/2)
	draw.Draw(sq, image.Rectangle{Min: off, Max: off.Add(image.Pt(ww, wh))}, web, image.Point{}, draw.Src)
	if err := encode(&set.Square, "square", sq, WebQuality); err != nil {
		return nil, err
	}
	tw, th := Fit(b.Dx(), b.Dy(), ThumbMaxSide)
	thumb := scale(web, tw, th)
	if err := encode(&set.Thumb, "thumb", thumb, ThumbQuality); err != nil {
		return nil, err
	}
	return set, nil
}

// flatten draws src on white (transparency becomes white).
func flatten(src image.Image) *image.RGBA {
	b := src.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	if o, ok := src.(interface{ Opaque() bool }); ok && o.Opaque() {
		draw.Draw(flat, flat.Bounds(), src, b.Min, draw.Src)
		return flat
	}
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), src, b.Min, draw.Over)
	return flat
}

// scale resizes an opaque image to w×h (Catmull-Rom); same size returns img.
func scale(img *image.RGBA, w, h int) *image.RGBA {
	b := img.Bounds()
	if w == b.Dx() && h == b.Dy() {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func encode(f *File, name string, img image.Image, quality int) error {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return fmt.Errorf("renditions: encode %s: %w", name, err)
	}
	sum := sha256.Sum256(buf.Bytes())
	b := img.Bounds()
	*f = File{Name: name, Data: buf.Bytes(), ContentType: "image/jpeg", Width: b.Dx(), Height: b.Dy(), SHA256: hex.EncodeToString(sum[:])}
	return nil
}

// jpegOrientation returns the EXIF orientation (1-8) of a JPEG, or 1.
func jpegOrientation(data []byte) int {
	if DetectContentType(data) != "image/jpeg" {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[i+2:]))
		if marker == 0xE1 && i+4+size-2 <= len(data) && size > 8 {
			if o := exifOrientation(data[i+4 : i+2+size]); o != 0 {
				return o
			}
		}
		i += 2 + size
	}
	return 1
}

func exifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := seg[6:]
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:8]))
	if off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			v := int(bo.Uint16(t[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
		}
	}
	return 0
}

// applyOrientation rotates/flips src according to an EXIF orientation.
func applyOrientation(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	var dst *image.RGBA
	if o >= 5 {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
