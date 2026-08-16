REM Copyright (c) 2025-now SuInk.
REM Licensed under the Limited Redistribution License in the repository root.

@echo off
setlocal
node "%~dp0dev.mjs" %*
exit /b %ERRORLEVEL%
