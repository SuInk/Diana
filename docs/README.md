# Diana Documentation Site

This directory contains the static documentation site published to GitHub Pages.
It has no build-time dependencies.

Preview locally from the repository root:

```sh
python3 -m http.server 4173 --directory docs
```

Then open `http://127.0.0.1:4173`.

The deployment workflow is `.github/workflows/pages.yml`.
