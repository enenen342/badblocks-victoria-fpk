# Badblocks Victoria for fnOS
飞牛系统硬盘坏道检测应用。调用系统 `badblocks -sv` 做只读扫描

## 功能
- 枚举系统内所有 `lsblk` 识别到的物理硬盘。
- 点击硬盘后启动只读坏道检测。
- 使用 SSE 实时返回 `badblocks` 输出、进度和状态。
- 支持停止当前扫描。
- 默认不使用 `badblocks -w`，不会写入或擦除硬盘。

## 飞牛系统使用方法
### 编译运行
下载仓库代码到飞牛系统本地，记住保存路径，运行以下命令：
```bash
cd /保存路径/badblocks-victoria-fpk

chmod +x cmd/*
chmod +x app/bin/fn-badblocks-victoria
chmod -R a+rX app cmd config
chmod a+r ICON.PNG ICON_256.PNG manifest

rm -f badblocks-victoria.fpk
fnpack build --directory .

```
进入飞牛系统应用市场，点击左下角手动安装，选择保存路径文件夹种badblocks-victoria.fpk文件安装

### fpk安装
下载release内fpk文件，进入飞牛系统应用市场，点击左下角手动安装，选择fpk文件上传即可

