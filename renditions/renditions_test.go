package renditions_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"testing"

	"github.com/openbrandexchange/obx-go/renditions"
	"github.com/openbrandexchange/obx-go/specdata"
)

func TestFixtureSizes(t *testing.T) {
	cases := []struct {
		file               string
		web, square, thumb [2]int
	}{
		{"kestrel/images/KO-1005_COG_1_front.png", [2]int{1200, 1500}, [2]int{1500, 1500}, [2]int{480, 600}},
		{"kestrel/images/KO-1002_OLV_1_front.png", [2]int{1600, 2000}, [2]int{2000, 2000}, [2]int{480, 600}},
	}
	for _, c := range cases {
		data, err := fs.ReadFile(specdata.Fixtures(), c.file)
		if err != nil {
			t.Fatal(err)
		}
		set, err := renditions.Generate(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []struct {
			got  *renditions.File
			want [2]int
		}{{&set.Web, c.web}, {&set.Square, c.square}, {&set.Thumb, c.thumb}} {
			if f.got.Width != f.want[0] || f.got.Height != f.want[1] {
				t.Errorf("%s %s: %dx%d, want %dx%d", c.file, f.got.Name, f.got.Width, f.got.Height, f.want[0], f.want[1])
			}
			if f.got.ContentType != "image/jpeg" || renditions.DetectContentType(f.got.Data) != "image/jpeg" {
				t.Errorf("%s %s is not a JPEG", c.file, f.got.Name)
			}
			sum := sha256.Sum256(f.got.Data)
			if hex.EncodeToString(sum[:]) != f.got.SHA256 {
				t.Errorf("%s %s: sha256 mismatch", c.file, f.got.Name)
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.got.Data))
			if err != nil || cfg.Width != f.got.Width || cfg.Height != f.got.Height {
				t.Errorf("%s %s: header says %dx%d (%v)", c.file, f.got.Name, cfg.Width, cfg.Height, err)
			}
		}
		again, _ := renditions.Generate(data)
		if again.Web.SHA256 != set.Web.SHA256 || again.Thumb.SHA256 != set.Thumb.SHA256 {
			t.Error("renditions are not deterministic")
		}
	}
}

func TestSmallImageNeverUpscaled(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	set, err := renditions.Generate(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if set.Web.Width != 300 || set.Thumb.Width != 300 || set.Square.Width != 300 || set.Square.Height != 300 {
		t.Fatalf("upscaled: web %dx%d thumb %dx%d square %dx%d", set.Web.Width, set.Web.Height, set.Thumb.Width, set.Thumb.Height, set.Square.Width, set.Square.Height)
	}
}

func TestTransparencyAndSquarePadding(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			if x >= 40 && x < 60 {
				img.Set(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	set, err := renditions.Generate(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	web, _ := jpeg.Decode(bytes.NewReader(set.Web.Data))
	if r, g, b, _ := web.At(5, 25).RGBA(); r>>8 < 245 || g>>8 < 245 || b>>8 < 245 {
		t.Fatalf("transparent area not white: %d %d %d", r>>8, g>>8, b>>8)
	}
	if r, _, _, _ := web.At(50, 25).RGBA(); r>>8 > 20 {
		t.Fatalf("opaque area lost: %d", r>>8)
	}
	sq, _ := jpeg.Decode(bytes.NewReader(set.Square.Data))
	if sq.Bounds().Dx() != 100 || sq.Bounds().Dy() != 100 {
		t.Fatalf("square is %v", sq.Bounds())
	}
	if r, _, _, _ := sq.At(50, 5).RGBA(); r>>8 < 245 {
		t.Fatal("square padding not white")
	}
	if r, _, _, _ := sq.At(50, 50).RGBA(); r>>8 > 20 {
		t.Fatal("square content not centred")
	}
}

func TestExifOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, nil)
	data := withOrientation(buf.Bytes(), 6)
	set, err := renditions.Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	if set.Web.Width != 20 || set.Web.Height != 40 {
		t.Fatalf("orientation 6 not applied: %dx%d", set.Web.Width, set.Web.Height)
	}
}

// withOrientation inserts an EXIF APP1 segment with the given orientation.
func withOrientation(jpg []byte, o uint16) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00*")
	binary.Write(&tiff, binary.BigEndian, uint32(8))
	binary.Write(&tiff, binary.BigEndian, uint16(1))
	binary.Write(&tiff, binary.BigEndian, uint16(0x0112))
	binary.Write(&tiff, binary.BigEndian, uint16(3))
	binary.Write(&tiff, binary.BigEndian, uint32(1))
	binary.Write(&tiff, binary.BigEndian, o)
	binary.Write(&tiff, binary.BigEndian, uint16(0))
	binary.Write(&tiff, binary.BigEndian, uint32(0))
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func TestRejectsNonImages(t *testing.T) {
	for _, in := range [][]byte{[]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), []byte("GIF89a"), nil} {
		if _, err := renditions.Generate(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if ct := renditions.DetectContentType([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")); ct != "image/webp" {
		t.Fatalf("webp magic: %q", ct)
	}
}

func TestFit(t *testing.T) {
	cases := [][5]int{{2400, 3000, 2000, 1600, 2000}, {3000, 2400, 2000, 2000, 1600}, {100, 50, 600, 100, 50}, {6000, 1, 600, 600, 1}}
	for _, c := range cases {
		w, h := renditions.Fit(c[0], c[1], c[2])
		if w != c[3] || h != c[4] {
			t.Errorf("Fit(%d,%d,%d) = %d,%d", c[0], c[1], c[2], w, h)
		}
	}
}
