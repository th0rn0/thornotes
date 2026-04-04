# Desktop icon assets

Place the following files here before building a distributable package.
The app runs fine without them (it uses a fallback), but packaging tools
need the icon files for the platform-specific bundle.

| File | Size | Used for |
| ---- | ---- | -------- |
| `icon.png` | 512×512 | Linux app icon, Windows fallback |
| `icon.icns` | (multi-resolution) | macOS `.app` bundle |
| `icon.ico` | (multi-resolution) | Windows installer |
| `tray-icon.png` | 16×16 (32×32 @2x) | System tray — all platforms |
| `tray-icon.ico` | 16×16 | System tray — Windows |

## Generating from the thornotes SVG

The master SVG is at `../web/static/icons/icon-512.svg`.

```sh
# Requires Inkscape and ImageMagick
inkscape ../web/static/icons/icon-512.svg --export-png=icon.png --export-width=512

# macOS .icns via iconutil (macOS only)
mkdir icon.iconset
for s in 16 32 64 128 256 512; do
  inkscape ../web/static/icons/icon-512.svg --export-png=icon.iconset/icon_${s}x${s}.png --export-width=$s
done
iconutil -c icns icon.iconset

# Windows .ico via ImageMagick
convert icon.png -resize 256x256 icon.ico

# Tray icon (16×16 with 2× variant)
inkscape ../web/static/icons/icon-512.svg --export-png=tray-icon.png --export-width=16
```
