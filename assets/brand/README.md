# Brand assets

Canonical brand pack — kept tracked so the project never loses its visual
identity to a gitignored design directory.

## Files

| File                  | Purpose                                          |
|-----------------------|--------------------------------------------------|
| `icon.svg`            | Icon-only mark (the crate "P"). Use for favicons, small chips, footer marks. |
| `icon-136.png`        | 1× raster of the icon (136×221) — Wails dock-size reference. |
| `icon-272.png`        | 2× retina raster. |
| `icon-544.png`        | 4× raster — README hero or marketing. |
| `wordmark.svg`        | Full lockup (icon + `PACKWRIGHT` wordmark + `HYBRID TUI · AWS INFRASTRUCTURE`). Use as the primary README hero. |
| `wordmark-648.png`    | 4× raster of the wordmark for non-SVG-rendering surfaces. |

## Palette

| Token   | Hex      | Role                                  |
|---------|----------|---------------------------------------|
| Base    | `#07090F` | Crate body — also the GUI app background. |
| Paper   | `#E2E8F0` | Card / lighter chrome.                |
| Accent  | `#F97316` | Primary orange (top label).           |
| Mid     | `#CC5E12` | Mid label.                            |
| Deep    | `#AD4F0D` | Bottom label.                         |
| Type    | `#F97316` / `#E2E8F0` | Wordmark fill (top / bottom row). |

## Where these are consumed

- `assets/logo.svg`, `assets/logo-icon.svg` — repo-root references for
  `README.md`. Mirrored from this directory; keep in sync.
- `gui/web/src/assets/logo.svg`, `gui/web/src/assets/logo-icon.svg` —
  imported by the Svelte launcher and sidebar.
- `gui/web/public/favicon*.png`, `apple-touch-icon.png`,
  `android-chrome-*.png`, `favicon.ico`, `site.webmanifest` — Vite
  static assets, referenced from `gui/web/index.html`.
- `build/appicon.png`, `build/darwin/*.icns` — Wails reads at package
  time to produce the macOS bundle icon.

## Source of truth

This directory **is** the source of truth for brand assets in the repo.
If the pre-export design files (Figma, etc.) ever produce new versions,
copy the rendered exports here and let the references downstream
(repo-root `assets/`, `gui/web/src/assets/`, `gui/web/public/`,
`build/appicon.png`) follow.
