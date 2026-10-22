#!/usr/bin/env bash
# Regenerates every icon from assets/logo.png (1024x1024, transparent background).
# The logo is mostly white, so wherever the background may be light it is shown on a
# dark rounded tile (assets/logo-tile.png). Requires rsvg-convert (librsvg).
set -euo pipefail
cd "$(dirname "$0")/.."

tile_svg() {
  # $1 = output size in px. Tile color matches the web UI dark background.
  cat <<SVG
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="$1" height="$1" viewBox="0 0 1024 1024">
  <defs><clipPath id="r"><rect width="1024" height="1024" rx="224"/></clipPath></defs>
  <g clip-path="url(#r)">
    <rect width="1024" height="1024" fill="#12151a"/>
    <image x="0" y="0" width="1024" height="1024" xlink:href="data:image/png;base64,$(base64 < assets/logo.png | tr -d '\n')"/>
  </g>
</svg>
SVG
}

render() { tile_svg "$1" | rsvg-convert -w "$1" -h "$1" -o "$2"; }

render 1024 assets/logo-tile.png
render 1024 client/build/appicon.png      # Wails derives the macOS/Windows icons from this
render 180  web/static/apple-touch-icon.png
render 64   web/static/favicon.png
render 64   web/static/logo-64.png         # header mark
render 128  client/frontend/logo.png       # desktop client connect page
echo "icons written"
