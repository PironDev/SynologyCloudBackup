// mkicon рисует иконки пакета (облако со стрелкой вверх) без внешних зависимостей.
//
//	go run ./packaging/tools/mkicon -out DIR -c1 FF7A59 -c2 E5391B
//
// Создаёт icon_{16,24,32,48,64,72,256}.png в DIR.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

func parseHex(s string) color.RGBA {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		log.Fatalf("неверный цвет %q (нужно RRGGBB)", s)
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

// circle/rounded-rect покрытие в нормированных координатах [0,1].
func inCircle(x, y, cx, cy, r float64) bool { return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r }

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Min(math.Max(x, x0+r), x1-r)
	cy := math.Min(math.Max(y, y0+r), y1-r)
	return inCircle(x, y, cx, cy, r)
}

func inCloud(x, y float64) bool {
	return inCircle(x, y, 0.36, 0.60, 0.15) ||
		inCircle(x, y, 0.52, 0.48, 0.19) ||
		inCircle(x, y, 0.68, 0.60, 0.14) ||
		(x >= 0.36 && x <= 0.68 && y >= 0.60 && y <= 0.75)
}

func inArrow(x, y float64) bool {
	// стрелка вверх внутри облака
	if x >= 0.47 && x <= 0.53 && y >= 0.52 && y <= 0.72 {
		return true
	}
	// наконечник — треугольник
	if y >= 0.40 && y <= 0.55 {
		half := (y - 0.40) / 0.15 * 0.10
		return math.Abs(x-0.50) <= half
	}
	return false
}

func render(size int, c1, c2 color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // суперсэмплинг для сглаживания
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					if !inRoundRect(x, y, 0.02, 0.02, 0.98, 0.98, 0.20) {
						continue
					}
					col := mix(c1, c2, (x+y)/2)
					if inCloud(x, y) {
						col = color.RGBA{255, 255, 255, 255}
						if inArrow(x, y) {
							col = mix(c1, c2, 0.6)
						}
					}
					r += float64(col.R)
					g += float64(col.G)
					b += float64(col.B)
					a++
				}
			}
			if a == 0 {
				continue
			}
			n := float64(ss * ss)
			img.SetRGBA(px, py, color.RGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / n * 255)})
		}
	}
	return img
}

func main() {
	out := flag.String("out", ".", "каталог вывода")
	h1 := flag.String("c1", "FF7A59", "цвет градиента 1 (RRGGBB)")
	h2 := flag.String("c2", "E5391B", "цвет градиента 2 (RRGGBB)")
	flag.Parse()
	c1, c2 := parseHex(*h1), parseHex(*h2)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, s := range []int{16, 24, 32, 48, 64, 72, 256} {
		f, err := os.Create(filepath.Join(*out, fmt.Sprintf("icon_%d.png", s)))
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(f, render(s, c1, c2)); err != nil {
			log.Fatal(err)
		}
		f.Close()
	}
}
