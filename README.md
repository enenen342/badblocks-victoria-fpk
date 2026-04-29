# Badblocks Victoria for fnOS

飞牛系统硬盘坏道检测应用。后端调用系统 `badblocks -sv` 做只读扫描，前端以 Victoria 风格显示硬盘列表、进度块、坏块计数和实时日志。

## 功能

- 枚举系统内所有 `lsblk` 识别到的物理硬盘。
- 点击硬盘后启动只读坏道检测。
- 使用 SSE 实时返回 `badblocks` 输出、进度和状态。
- 支持停止当前扫描。
- 默认不使用 `badblocks -w`，不会写入或擦除硬盘。

## 构建

需要 Go 1.22+ 和飞牛官方 `fnpack`。

Linux/macOS/WSL:

```sh
sh scripts/build-linux.sh
```

Windows PowerShell:

```powershell
.\scripts\build-windows.ps1
```

构建成功后，二进制位于 `app/bin/fn-badblocks-victoria`。如果已安装 `fnpack`，`.fpk` 会输出到 `dist/`。

## 本地运行

```sh
go run ./src -addr 127.0.0.1:24046 -ui ./ui
```

打开 `http://127.0.0.1:24046`。

## 注意

扫描整个大容量硬盘会非常耗时。检测系统盘或正在使用的阵列盘时，建议先确认备份和业务窗口。
