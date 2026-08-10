# Diana Documentation Site

This directory contains the dependency-free, multi-page documentation site published to GitHub Pages.

Pages:

- `index.html`: product overview and quick start
- `deploy.html`: one-click installer, Release, Docker, and source deployment
- `configuration.html`: channels, models, groups, and Agent tools
- `implementation.html`: architecture, message flow, memory, media, and storage
- `operations.html`: updates, backup, troubleshooting, and development

Preview locally from the repository root:

```sh
python3 -m http.server 4173 --directory docs
```

Then open `http://127.0.0.1:4173`.

The deployment workflow is `.github/workflows/pages.yml`.
