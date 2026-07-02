package qr

import (
	"github.com/skip2/go-qrcode"
)

func GenerateQR(data string) ([]byte, error) {
	png, err := qrcode.Encode(data, qrcode.Medium, 256)
	if err != nil {
		return nil, err
	}
	return png, nil
}
