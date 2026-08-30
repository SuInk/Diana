# Diana Documentation Site

This directory contains the dependency-free, multi-page documentation site published to GitHub Pages.

Pages:

- `index.html`: product overview and quick start
- `deploy.html`: one-click installer, Release, Docker, and source deployment
- `configuration.html`: channels, models, groups, and Agent tools
- `implementation.html`: architecture, message flow, memory, media, and storage
- `operations.html`: updates, backup, troubleshooting, and development
- `demo/`: the real `frontend-next` WebUI built in demo mode with local mock API data
- `en/`: the English site — same five pages, sharing the stylesheets and scripts above
- `landing.css` / `landing.js`: shared by both landing pages
- `styles.css` / `app.js` / `theme.js`: shared by all documentation pages, in both languages

Pages link to each other without a file extension (`/deploy`, not `/deploy.html`); GitHub Pages
resolves those to the matching `.html`. A local preview therefore needs a server that does the
same — `python3 -m http.server` returns 404 for them.

Preview locally from the repository root:

```sh
npx serve docs
```

Then open the address it prints.

The deployment workflow is `.github/workflows/pages.yml`.
