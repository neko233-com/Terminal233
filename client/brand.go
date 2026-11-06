package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

//go:embed resources/icon.png
var nekoLogoPNG []byte

var nekoLogo = func() *ui.Bitmap {
	logo, err := ui.DecodeBitmap(nekoLogoPNG)
	if err != nil {
		panic("invalid embedded NekoSSH logo: " + err.Error())
	}
	return logo
}()
