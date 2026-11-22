package app

import (
	"github.com/egoist/mygo/ui"

	"mygo-music/resources"
)

var brandMark = func() *ui.Bitmap {
	bitmap, err := ui.DecodeBitmap(resources.Icon)
	if err != nil {
		panic("decode app icon: " + err.Error())
	}
	return bitmap
}()

func (a *app) sidebarBrand(c *ui.Context) {
	ui.Row(c.Key("brand")).Height(40).Shrink(0).PaddingX(8).Gap(9).AlignItems(ui.Center).Margin(0, 0, 8, 0).Children(func() {
		ui.Image(c, brandMark).Size(28, 28).Radius(6).Shrink(0)
		ui.Text(c, "Oh MyGo Music").FontSize(18).FontWeight(700).LetterSpacing(-0.4).TextColor(a.pal.text).SingleLine()
	})
}
