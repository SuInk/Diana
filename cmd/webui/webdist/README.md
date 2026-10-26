发布构建前把 `frontend/dist` 的内容复制到这里，前端就会被 `//go:embed` 编进
diana-webui，一个可执行文件即可运行。源码里只留这份说明：目录里没有
`index.html` 时，程序退回从磁盘读取前端（`frontend/dist` 等），方便本地开发。
