构建前把 `frontend/dist` 的内容复制到这里（`scripts/embed-frontend.sh`，`make build`
会自动做），前端就会被 `//go:embed` 编进 diana-webui，程序只用这份内嵌前端。
源码里只留这份说明：没复制就编出来的程序打不开页面，启动日志和 `diana doctor`
会提示；本地开发用 `make dev`，页面由 Vite 提供。
