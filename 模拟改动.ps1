# 向指定文件原地写入若干小段随机数据（长度不变），用于验证增量同步仅写入受影响的块。
# 用法示例：
#   powershell -ExecutionPolicy Bypass -File 模拟改动.ps1 "D:\data\project\part-001.bin"
#   powershell -ExecutionPolicy Bypass -File 模拟改动.ps1 "D:\data\project\part-001.bin" -Count 8 -Bytes 8192 -DryRun
#   加 -NoAlign 则不再保证每处落在不同块内（更接近随机写入），可观察跨块情况。
# 注意：会真实改坏文件内容，请只对测试副本使用；加 -Backup 会先复制一份 .bak。
param(
    [Parameter(Mandatory = $true, Position = 0)][string]$Path,
    [int]$Count = 5,
    [int]$Bytes = 4096,
    [int]$BlockSize = 32768,
    [int]$Seed = 0,
    [switch]$NoAlign,
    [switch]$DryRun,
    [switch]$Backup
)

$Align = -not $NoAlign

$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false } catch { }

$Path = [System.IO.Path]::GetFullPath($Path)
if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "文件不存在: $Path" }
if ($Count -lt 1) { throw "-Count 必须 >= 1" }
if ($Bytes -lt 1) { throw "-Bytes 必须 >= 1" }
if ($BlockSize -lt 512) { throw "-BlockSize 过小" }

$fi = Get-Item -LiteralPath $Path
$size = [int64]$fi.Length
$blocks = [int64][math]::Ceiling($size / $BlockSize)
if ($blocks -lt 1) { throw "文件为空，无法改写" }

if ($Align) {
    if ($blocks -lt $Count) { throw "文件太小：只有 $blocks 个块，放不下 $Count 处互不重叠的改动" }
    if ($Bytes -ge $BlockSize) { throw "对齐模式下 -Bytes 必须小于 -BlockSize（$BlockSize）" }
} else {
    if ($size -lt ($Bytes + 1)) { throw "文件太小，无法写入 $Bytes 字节" }
}

if ($Seed -eq 0) { $Seed = [int](Get-Random -Minimum 1 -Maximum 2000000000) }
$rnd = [System.Random]::new($Seed)
$mtimeBefore = $fi.LastWriteTime

Write-Host ""
Write-Host "文件   : $Path"
Write-Host ("大小   : {0:N0} 字节（{1:N1} MiB），{2:N0} 个 {3:N0} 字节的块" -f $size, ($size / 1MB), $blocks, $BlockSize)
Write-Host ("参数   : {0} 处 x {1:N0} 字节，对齐={2}，随机种子={3}" -f $Count, $Bytes, $Align, $Seed)

# 选定位置：对齐模式保证每处改动落在互不相同的块内部
$plan = New-Object System.Collections.Generic.List[object]
if ($Align) {
    $picked = New-Object System.Collections.Generic.List[int64]
    $seen = @{}
    while ($picked.Count -lt $Count) {
        $b = [int64]($rnd.NextDouble() * $blocks)
        if ($seen.ContainsKey([string]$b)) { continue }
        $seen[[string]$b] = 1
        $picked.Add($b)
    }
    $picked.Sort()
    foreach ($b in $picked) {
        $room = $BlockSize - $Bytes
        $inner = [int64](1 + $rnd.NextDouble() * ($room - 2))
        if ($inner -lt 1) { $inner = 1 }
        $off = $b * $BlockSize + $inner
        if ($off + $Bytes -gt $size) { $off = $size - $Bytes }
        $plan.Add([pscustomobject]@{ Offset = $off; Length = $Bytes })
    }
} else {
    for ($i = 0; $i -lt $Count; $i++) {
        $off = [int64]($rnd.NextDouble() * ($size - $Bytes))
        $plan.Add([pscustomobject]@{ Offset = $off; Length = $Bytes })
    }
}

# 统计受影响的块
$touched = @{}
foreach ($c in $plan) {
    $first = [int64][math]::Floor($c.Offset / $BlockSize)
    $last = [int64][math]::Floor(($c.Offset + $c.Length - 1) / $BlockSize)
    for ($b = $first; $b -le $last; $b++) { $touched[[string]$b] = 1 }
}
$expected = 0
foreach ($k in $touched.Keys) {
    $b = [int64]$k
    $blen = [math]::Min($BlockSize, $size - $b * $BlockSize)
    $expected += $blen
}

Write-Host ""
Write-Host "序号    偏移                 块号     块内偏移    长度     跨块"
Write-Host "------  ------------------   ------   ---------   ------   ----"
$i = 0
foreach ($c in $plan) {
    $i++
    $b = [int64][math]::Floor($c.Offset / $BlockSize)
    $last = [int64][math]::Floor(($c.Offset + $c.Length - 1) / $BlockSize)
    $span = if ($last -gt $b) { "$($b)-$last" } else { "否" }
    Write-Host ("{0,6}  {1,18:N0}   {2,6}   {3,9:N0}   {4,6:N0}   {5}" -f $i, $c.Offset, $b, ($c.Offset % $BlockSize), $c.Length, $span)
}

$dataBytes = [int64]$Count * $Bytes
Write-Host ""
Write-Host ("数据合计     : {0:N0} 字节（{1:N1} KiB）" -f $dataBytes, ($dataBytes / 1KB))
Write-Host ("受影响块     : {0} 个（共 {1:N0} 字节）" -f $touched.Count, $expected)
Write-Host ("预期同步写入 : {0:N0} 字节（{1:N1} KiB），放大 {2:N2} 倍" -f $expected, ($expected / 1KB), ($expected / [double]$dataBytes))

if ($DryRun) {
    Write-Host ""
    Write-Host "-DryRun：未写入任何内容" -ForegroundColor Yellow
    Write-Host ("想真正执行就去掉 -DryRun；随机种子 {0} 可复现同一组位置。" -f $Seed)
    exit 0
}

if ($Backup) {
    $bak = "$Path.bak"
    Copy-Item -LiteralPath $Path -Destination $bak -Force
    Write-Host ""
    Write-Host "已备份到 $bak" -ForegroundColor Yellow
}

$fs = [System.IO.File]::Open($Path, 'Open', 'ReadWrite')
try {
    $buf = New-Object byte[] $Bytes
    foreach ($c in $plan) {
        $rnd.NextBytes($buf)
        $fs.Seek($c.Offset, 'Begin') | Out-Null
        $fs.Write($buf, 0, $c.Length)
    }
    $fs.Flush()
} finally {
    $fs.Close()
}

$after = (Get-Item -LiteralPath $Path)
Write-Host ""
if ([int64]$after.Length -ne $size) {
    Write-Host ("前提不满足：写入后文件长度变化 {0:N0} -> {1:N0}（可能有其他程序同时写入该文件）" -f $size, $after.Length) -ForegroundColor Yellow
} else {
    Write-Host ("已写入，长度未变（{0:N0} 字节）" -f $after.Length) -ForegroundColor Green
}
Write-Host ("修改时间：{0} -> {1}" -f $mtimeBefore.ToString('HH:mm:ss'), $after.LastWriteTime.ToString('HH:mm:ss'))

$dir = Split-Path -Parent $Path
Write-Host ""
Write-Host "下一步可查看同步将传输的数据量（不写入任何文件）：" -ForegroundColor Cyan
Write-Host ("  dist\BlockSync-CLI.exe sync `"{0}`" `"<输出目录>`" --dry-run" -f $dir)
Write-Host ("预期那行会显示 变化块 约 {0:N1} KiB" -f ($expected / 1KB))
