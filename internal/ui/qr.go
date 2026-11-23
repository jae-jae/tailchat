package ui

import (
	"fmt"
	"image/color"

	mygoui "github.com/egoist/mygo/ui"
	qrcode "github.com/skip2/go-qrcode"
)

const defaultPairingQRSize = 384

// renderPairingQRBitmap generates a crisp pixel-aligned QR code bitmap in pure Go.
func renderPairingQRBitmap(code string, size int) (*mygoui.Bitmap, error) {
	if code == "" {
		return nil, fmt.Errorf("pairing code is empty")
	}
	if size <= 0 {
		size = defaultPairingQRSize
	}

	qr, err := qrcode.New(code, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("encode pairing qr: %w", err)
	}
	qr.ForegroundColor = color.NRGBA{R: 0x0F, G: 0x17, B: 0x2A, A: 0xFF}
	qr.BackgroundColor = color.White
	qr.DisableBorder = true

	img := qr.Image(size)
	return mygoui.NewBitmap(img), nil
}
