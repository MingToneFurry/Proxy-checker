@echo off
setlocal

set ROOT=%~dp0
pushd "%ROOT%" >nul

echo === building windows amd64 ===
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -v -o proxy-checker.exe .

echo.
echo === building linux amd64 (optimized for memory) ===
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -v -ldflags="-s -w" -o proxy-checker .

echo.
echo === building linux arm64 ===
set GOOS=linux
set GOARCH=arm64
set CGO_ENABLED=0
go build -v -ldflags="-s -w" -o proxy-checker-arm64 .
